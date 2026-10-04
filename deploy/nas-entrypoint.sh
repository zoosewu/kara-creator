#!/bin/sh
# NAS container 的入口：
#   - PUID / PGID：用這個身分執行，寫出的檔案擁有者才對得上 NAS 的使用者（SMB 那邊改得動）。沒設就用 root
#     （Mac 的 OrbStack 寫進 bind mount 的檔案本來就屬於 Mac 的使用者）
#   - /ssh：資料備份 push 用的 SSH 金鑰（唯讀掛載）。複製到家目錄、權限改成 600，ssh 才肯用
set -e

if [ -d /ssh ] && [ "$(ls -A /ssh 2>/dev/null)" ]; then
  mkdir -p "$HOME/.ssh"
  cp -R /ssh/. "$HOME/.ssh/"
  chmod 700 "$HOME/.ssh"
  find "$HOME/.ssh" -type f -exec chmod 600 {} +
  # 第一次連線自動記下主機金鑰（之後主機金鑰變了照樣會拒絕）
  grep -qs StrictHostKeyChecking "$HOME/.ssh/config" || printf 'Host *\n  StrictHostKeyChecking accept-new\n' >> "$HOME/.ssh/config"
fi

if [ -n "$PUID" ] && [ "$(id -u)" = 0 ]; then
  PGID=${PGID:-$PUID}
  # yt-dlp 每天自己更新，放工具的資料夾要寫得進去
  chown -R "$PUID:$PGID" "$HOME" "$KARA_TOOLS"
  if ! setpriv --reuid="$PUID" --regid="$PGID" --clear-groups test -w "$KARA_LIBRARY"; then
    echo "uid $PUID / gid $PGID 不能寫入曲庫 $KARA_LIBRARY（擁有者：$(stat -c %u:%g "$KARA_LIBRARY")）。" >&2
    echo "請把 PUID / PGID 設成曲庫資料夾的擁有者（在 NAS 上用 id 或 ls -ln 查）。" >&2
    exit 1
  fi
  exec setpriv --reuid="$PUID" --regid="$PGID" --clear-groups --inh-caps=-all kara-nas "$@"
fi
exec kara-nas "$@"
