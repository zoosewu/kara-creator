# kara-creator — 伴唱帶工作室

把 YouTube 影片（或自己的影音檔）做成 KTV 伴唱帶：**下載 → 去人聲 → 逐字對時 → 燒上逐字變色的字幕**。
在網頁上管理曲庫、編輯歌詞、邊看邊調時間，確認沒問題的歌一鍵匯出到給卡拉 OK 軟體用的資料夾。

> 給 AI 助手：先讀 [AGENTS.md](AGENTS.md)。

## 架構

| | 做什麼 | 放哪裡 |
| --- | --- | --- |
| **NAS 伺服器** | 網頁 UI、曲庫、下載、排程、匯出、資料備份 | 放曲庫的那台（Mac mini、Linux NAS） |
| **AI 伺服器** | 去人聲、對時、燒錄字幕、對時檢查 | 有 NVIDIA 顯示卡的電腦 |

AI 伺服器主動連到 NAS 領工作，NAS 不必知道它在哪裡；可以有好幾台，也可以暫時不開（工作會排隊等它）。

## 部署 NAS 伺服器（docker compose）

需要 Docker（Mac 用 [OrbStack](https://orbstack.dev/)）。在一個空資料夾裡：

```sh
curl -LO https://raw.githubusercontent.com/zoosewu/kara-creator/main/deploy/compose.nas.yml
cat > .env <<'EOF'
KARA_LIBRARY_DIR=/Volumes/外接硬碟/kara     # 曲庫資料夾
KARA_WORKER_TOKEN=自己訂的密碼                # AI 伺服器要填一樣的（選填）
# KARA_SSH_DIR=/Users/me/.ssh/kara-backup   # 資料備份 push 用的 SSH 金鑰資料夾（選填）
# PUID=1000                                 # Linux NAS：曲庫資料夾擁有者的 uid / gid（Mac 不用設）
# PGID=1000
EOF

docker compose -f compose.nas.yml run --rm kara-nas --init   # 只有第一次：建立新的曲庫，看到「NAS 伺服器啟動」後按 Ctrl+C
docker compose -f compose.nas.yml up -d
```

打開 `http://<這台>:8765`。更新：`docker compose -f compose.nas.yml pull && docker compose -f compose.nas.yml up -d`。

- 曲庫資料夾裡會有 `songs/`（每首歌的檔案）、`export/`（匯出的伴唱帶）、`inbox/`（把影音檔丟進來就會自動加入曲庫）、
  `fonts/`（自訂字型）、`data/`（資料備份；是 git repo 時會自動 commit、有 remote 就 push）
- 外接硬碟沒掛上時伺服器不會啟動（避免開出一個空的曲庫）
- Mac 記得在「系統設定 → 能源」開啟「顯示器關閉時防止自動進入睡眠」，否則 AI 伺服器連不到

## 部署 AI 伺服器

兩種方式擇一。名稱（`KARA_WORKER_NAME` / `--name`）會顯示在網頁的「AI 伺服器」清單。

### docker compose（建議）

需要 NVIDIA 驅動與支援 GPU 的 Docker（Windows：Docker Desktop + WSL2）。在一個空資料夾裡：

```sh
curl -LO https://raw.githubusercontent.com/zoosewu/kara-creator/main/deploy/compose.ai.yml
cat > .env <<'EOF'
KARA_NAS=http://mac-mini.local:8765
KARA_WORKER_NAME=pc-4070
KARA_WORKER_TOKEN=自己訂的密碼
EOF

docker compose -f compose.ai.yml up -d
```

映像約 9 GB；第一次處理時再下載模型（約 5 GB，存在 volume `kara-models`，更新映像不必重新下載）。

### 直接在 Windows 上執行

需要 NVIDIA 顯示卡（CUDA 13 驅動）與 Python 3.14（`winget install Python.Python.3.14`）。

```powershell
git clone https://github.com/zoosewu/kara-creator.git
cd kara-creator\worker
.\setup.ps1                   # 下載 ffmpeg、建立 Python 環境（PyTorch CUDA 版）、安裝 kara-worker
.\worker.ps1 --nas http://mac-mini.local:8765 --name pc-4070 --token 自己訂的密碼
```

更新：`git pull` 後再執行一次 `setup.ps1`，然後啟動 `worker.ps1`。其他參數見 `.\worker.ps1 --help`（例如 `--channels`、`--device cpu`）。

## 使用

1. 貼上影片網址，按「製作伴唱帶」（可以順便附上歌詞；也可以之後在「歌詞」裡輸入）
2. 等它下載、去人聲、對時、燒錄完成；有疑慮的句子會標出來，點了直接跳到那一句播放
3. 在播放畫面邊看邊調時間，沒問題就按「確認沒問題」
4. 按曲庫上方的「匯出」，已確認的歌會放到 `export/`（資料夾結構同曲庫，檔名「歌手 - 歌名.mp4」）

歌詞一行一句，照實際演唱順序寫完整（重複的副歌要重寫）。演唱者、讀音、翻譯都可以在歌詞編輯器裡用點的完成。

## 映像

CI 在 `main` 有變動時自動建置並推到 GitHub Container Registry：

| 映像 | 架構 |
| --- | --- |
| `ghcr.io/zoosewu/kara-creator-nas` | linux/amd64、linux/arm64 |
| `ghcr.io/zoosewu/kara-creator-ai` | linux/amd64（CUDA） |

標籤：`latest`（main 最新）、`sha-xxxxxxx`（某一次 commit）、`1.2.3`（推 `v1.2.3` tag 時）。
要固定版本就在 `.env` 加 `KARA_TAG=sha-xxxxxxx`。兩邊的版本要一致（演算法版本不同時 AI 伺服器會被拒絕）。

第一次發布後如果 `docker compose pull` 出現 `denied`：到 GitHub 的 Packages 把這兩個套件的 visibility 設成 Public。

## 開發

| 目錄 | 內容 |
| --- | --- |
| `nas/` | NAS 伺服器（Go）與網頁前端（`nas/web/`，Svelte） |
| `worker/` | AI 伺服器（Python 套件 `kara_worker`） |
| `deploy/` | Dockerfile 與 compose |
| `docs/` | 設計文件與 API 規格（[docs/README.md](docs/README.md)） |

開發環境與測試方法見 [docs/deploy.md](docs/deploy.md)。

## 授權

程式碼以 [GNU AGPL-3.0](LICENSE) 授權。映像內的 ffmpeg、yt-dlp、deno、Noto Sans CJK 字型與各 Python 套件依其各自的授權。
歌曲、影片與歌詞的著作權屬於原作者，本專案不包含也不散布這些內容。
