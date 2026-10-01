"use strict";

// ---- 小工具 -----------------------------------------------------------------

const $ = (sel, root = document) => root.querySelector(sel);
const enc = encodeURIComponent;

/** 建立 DOM 節點；文字一律當純文字處理，不會被解讀成 HTML。 */
function el(tag, attrs = {}, ...children) {
  const svgTag = tag === "svg" || tag === "use";
  const node = svgTag ? document.createElementNS("http://www.w3.org/2000/svg", tag) : document.createElement(tag);
  for (const [key, value] of Object.entries(attrs)) {
    if (value === null || value === undefined || value === false) continue;
    if (key === "class") node.setAttribute("class", value);
    else if (key === "dataset") Object.assign(node.dataset, value);
    else if (key.startsWith("on")) node.addEventListener(key.slice(2), value);
    else node.setAttribute(key, value === true ? "" : value);
  }
  for (const child of children.flat()) {
    if (child === null || child === undefined || child === false) continue;
    node.append(child instanceof Node ? child : document.createTextNode(String(child)));
  }
  return node;
}

const icon = (name) => el("svg", { "aria-hidden": "true" }, el("use", { href: `#i-${name}` }));

async function api(path, { method = "GET", body } = {}) {
  const init = { method, headers: {} };
  if (body !== undefined) {
    init.headers["Content-Type"] = "application/json";
    init.body = JSON.stringify(body);
  }
  const res = await fetch(path, init);
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(typeof data.detail === "string" ? data.detail : `HTTP ${res.status}`);
  return data;
}

let toastTimer;
function toast(message, error = false) {
  const box = $("#toast");
  box.textContent = message;
  box.className = "toast" + (error ? " error" : "");
  box.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => (box.hidden = true), error ? 5000 : 2500);
}

function fmtDuration(seconds) {
  if (!seconds) return "";
  const s = Math.round(seconds);
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
}

function fmtTime(ts) {
  return new Date(ts * 1000).toLocaleTimeString("zh-TW", { hour12: false, hour: "2-digit", minute: "2-digit" });
}

/** 和後端 export.file_name 相同的規則，用來即時預覽輸出檔名。 */
function exportName(title, artist) {
  const raw = artist?.trim() ? `${artist.trim()} - ${title.trim()}` : title.trim();
  const name = raw.replace(/[<>:"/\\|?*]/g, "＿").replace(/[\x00-\x1f]/g, "").replace(/[ .]+$/, "");
  return `${name.slice(0, 150)}.mp4`;
}

// ---- 主題 -------------------------------------------------------------------

$("#theme-toggle").addEventListener("click", () => {
  const next = document.documentElement.dataset.theme === "dark" ? "light" : "dark";
  document.documentElement.dataset.theme = next;
  try { localStorage.setItem("theme", next); } catch (e) { /* 無痕模式等情況存不了就算了 */ }
});

// ---- 狀態 -------------------------------------------------------------------

const LANGUAGE_NAMES = { zh: "國語", nan: "台語", yue: "粵語", ja: "日文", en: "英文" };
const LANGUAGE_HINTS = {
  "": "依歌詞文字判斷（假名 → 日文、漢字 → 國語、英文字母 → 英文）。",
  nan: "台語用 CTC 對時（不經 Whisper）。請在歌詞的「標註」模式點每個漢字標上台羅（例如 我 → guá），"
    + "沒標的字會用國語拼音代替、對得比較不準。對時檢查只做規則檢查。",
  yue: "粵語用 CTC 對時（不經 Whisper）。請在歌詞的「標註」模式點每個漢字標上粵拼（例如 我 → ngo5），"
    + "沒標的字會用國語拼音代替、對得比較不準。對時檢查只做規則檢查。",
};
const STEP_NAMES = { download: "下載", separate: "去人聲", karaoke: "製作伴唱帶", check: "檢查對時", retime: "AI 重新對時", wrapup: "收尾" };
const STAGE_BY_NAME = Object.fromEntries(Object.entries(STEP_NAMES).map(([k, v]) => [v, k]));
const STAGES = [["download", "來源"], ["separate", "去人聲"], ["lyrics", "歌詞"], ["karaoke", "伴唱帶"]];

const state = {
  items: [], folders: [], jobs: [],
  renderKey: "", jobsKey: "",
  selectedJob: null, logOffset: 0,
  folder: null,             // 選取的資料夾 id（新歌存入這裡），null 為最上層
  collapsed: new Set(),     // 收合的資料夾 id
  drag: null,               // 拖曳中的 { type: "folder" | "song", id, parent }
  selected: new Set(),      // 批次操作勾選的歌曲 key
  lastPick: null,           // 上一次勾選的歌曲（Shift 連續勾選用）
};

try {
  state.folder = localStorage.getItem("folder") || null;
  state.collapsed = new Set(JSON.parse(localStorage.getItem("collapsed") || "[]"));
} catch (e) { /* ignore */ }

const folderById = (id) => state.folders.find((f) => f.id === id);

function remember() {
  try {
    if (state.folder) localStorage.setItem("folder", state.folder); else localStorage.removeItem("folder");
    localStorage.setItem("collapsed", JSON.stringify([...state.collapsed]));
  } catch (e) { /* ignore */ }
}

async function loadState() {
  const data = await api("/api/state");
  Object.assign(state, { items: data.items, folders: data.folders, jobs: data.jobs, settings: data.settings });
  if (state.folder && !folderById(state.folder)) state.folder = null;  // 資料夾被刪掉了
  const keys = new Set(data.items.map((i) => i.key));
  for (const k of state.selected) if (!keys.has(k)) state.selected.delete(k);  // 歌曲不見了
  // 拖曳或原位編輯途中重繪會打斷操作，等結束後再更新。
  if (!state.drag && !state.editing) {
    const key = JSON.stringify([data.items, data.folders, state.folder, [...state.collapsed]]);
    if (key !== state.renderKey) {
      state.renderKey = key;
      renderLibrary();
    }
  }
  const jobsKey = JSON.stringify(data.jobs);
  if (jobsKey !== state.jobsKey) {
    state.jobsKey = jobsKey;
    renderJobs();
  }
  if (!state.selectedJob && data.jobs.length) selectJob(data.jobs[0].id);
  watchStudioJob(data.jobs).catch(() => {});
  await pollLog();
}

async function refresh() {
  try { await loadState(); } catch (e) { toast(e.message, true); }
}

async function loop() {
  try { await loadState(); } catch (e) { /* 伺服器暫時沒回應時下次再試 */ }
  setTimeout(loop, 1500);
}

function rerender() {
  state.renderKey = "";
  renderLibrary();
}

// ---- 曲庫（樹狀） -----------------------------------------------------------

function selectFolder(id) {
  state.folder = id;
  if (id) state.collapsed.delete(id);  // 選到的資料夾順便展開
  remember();
  rerender();
}

function toggleFolder(id) {
  if (state.collapsed.has(id)) state.collapsed.delete(id); else state.collapsed.add(id);
  remember();
  rerender();
}

function renderLibrary() {
  const made = state.items.filter((i) => i.stages.karaoke === "done" || i.approval).length;
  const approved = state.items.filter((i) => i.approval === "approved").length;
  $("#lib-count").textContent = state.items.length
    ? `${state.items.length} 首` + (made ? ` · 已確認 ${approved} / ${made}` : "") : "";
  $("#lib-root").classList.toggle("selected", state.folder === null);
  const current = folderById(state.folder);
  $("#target-folder").textContent = `新歌存入：${current ? current.path_label : "曲庫最上層"}`;

  renderBatchBar();
  const box = $("#items");
  box.replaceChildren();
  if (!state.items.length && !state.folders.length) {
    box.append(el("div", { class: "empty" }, "還沒有歌曲。在上方貼上網址，按「製作伴唱帶」開始。"));
    return;
  }
  renderLevel(box, null, 0);
}

/** 依序畫出某一層的資料夾（展開的話連同內容）與歌曲。 */
function renderLevel(box, parent, depth) {
  for (const folder of state.folders.filter((f) => f.parent === parent)) {
    box.append(renderFolder(folder, depth));
    if (!state.collapsed.has(folder.id)) renderLevel(box, folder.id, depth + 1);
  }
  for (const item of state.items.filter((i) => i.folder === parent)) box.append(renderSong(item, depth));
}

function renderFolder(folder, depth) {
  const open = !state.collapsed.has(folder.id);
  const empty = !folder.songs && !folder.folders;
  const counts = [folder.songs ? `${folder.songs} 首` : null, folder.folders ? `${folder.folders} 個資料夾` : null]
    .filter(Boolean).join(" · ") || "空的";
  const row = el("div", {
    class: "folder-row tree-row" + (state.folder === folder.id ? " selected" : ""),
    style: `--depth:${depth}`,
    title: "點一下選取（新歌存入這裡）；可拖曳到其他資料夾",
    onclick: () => selectFolder(folder.id),
  },
  el("button", {
    type: "button", class: "caret" + (open ? " open" : "") + (empty ? " leaf" : ""),
    "aria-label": open ? "收合" : "展開",
    onclick: (e) => { e.stopPropagation(); toggleFolder(folder.id); },
  }, icon("caret")),
  pickBox(songsUnder(folder.id), `勾選「${folder.name}」裡的所有歌曲（含子資料夾）`),
  el("svg", { class: "folder-icon", "aria-hidden": "true" }, el("use", { href: "#i-folder" })),
  el("span", { class: "title-line" },
    el("span", { class: "folder-name" }, folder.label),
    el("button", {
      type: "button", class: "edit-btn", title: "重新命名", "aria-label": "重新命名",
      onclick: (e) => {
        e.stopPropagation();
        startInlineEdit(e.currentTarget.parentElement, row, {
          value: folder.name, placeholder: "資料夾名稱",
          onSave: (name) => name && api(`/api/folders/${folder.id}`, { method: "PATCH", body: { name } }),
        });
      },
    }, icon("edit"))),
  el("span", { class: "folder-meta grow" }, counts),
  el("div", { class: "row-tools" },
    el("button", {
      type: "button", class: "view-link", title: "在這個資料夾裡新增子資料夾",
      onclick: (e) => { e.stopPropagation(); openFolderDialog(null, folder.id); },
    }, icon("plus"), "子資料夾"),
    el("button", {
      type: "button", class: "view-link",
      onclick: (e) => { e.stopPropagation(); openFolderDialog(folder); },
    }, icon("tag"), "編輯")));
  makeDraggable(row, { type: "folder", id: folder.id, parent: folder.parent });
  makeDropTarget(row, { kind: "folder", id: folder.id, parent: folder.parent });
  return row;
}

function stageView(key, value, running) {
  const label = Object.fromEntries(STAGES)[key];
  if (running) return { cls: "running", text: `${label}中`, tip: "處理中" };
  switch (value) {
    case "done": return { cls: "done", text: label, tip: "已完成" };
    case "outdated": return { cls: "outdated", text: `${label}需更新`, tip: "來源、歌詞或歌曲資訊有變動，重新製作即可更新" };
    case "missing": return { cls: "missing", text: "缺歌詞", tip: "按「輸入歌詞」" };
    case "no_lyrics": return { cls: "pending", text: label, tip: "需要先有歌詞" };
    default: return { cls: "pending", text: label, tip: "尚未處理" };
  }
}

function renderSong(item, depth) {
  const job = item.job;
  const busy = Boolean(job);
  const runningKey = job && job.status === "running" ? STAGE_BY_NAME[job.stage] : null;
  const hasLyrics = item.stages.lyrics === "done";

  const stages = el("ol", { class: "stages" }, STAGES.map(([key]) => {
    const view = stageView(key, item.stages[key], runningKey === key);
    return el("li", { class: `stage ${view.cls}`, title: view.tip }, el("i"), view.text);
  }));

  // 執行：只列出還需要做的步驟，已完成的就不顯示。
  const run = [];
  if (item.stages.separate !== "done") {
    run.push(el("button", {
      type: "button", class: "run-btn", disabled: busy,
      title: "排入佇列：把人聲和伴奏分開", onclick: () => runSteps(item, ["separate"]),
    }, icon("play"), "去人聲"));
  }
  if (item.stages.karaoke !== "done") {
    run.push(el("button", {
      type: "button", class: "run-btn", disabled: busy || !hasLyrics,
      title: hasLyrics ? "排入佇列：對時、產生字幕並輸出伴唱帶" : "需要先輸入歌詞",
      onclick: () => runSteps(item, ["karaoke"]),
    }, icon("play"), item.stages.karaoke === "outdated" ? "更新伴唱帶" : "製作伴唱帶"));
  }

  // 查看 / 整理：只開畫面，不會啟動處理。
  const view = [
    el("button", { type: "button", class: "view-link" + (hasLyrics ? "" : " attention"), onclick: () => openEditor(item) },
      icon("lyrics"), hasLyrics ? "歌詞" : "輸入歌詞"),
    el("button", { type: "button", class: "view-link", onclick: () => openSongDialog(item) }, icon("tag"), "資訊"),
    el("button", { type: "button", class: "view-link", title: "在檔案總管開啟", onclick: () => revealItem(item) },
      icon("folder"), "檔案位置"),
    el("button", {
      type: "button", class: "view-link", title: "重新處理…", "aria-haspopup": "menu",
      onclick: (e) => openRedoMenu(e.currentTarget, item),
    }, icon("more")),
  ];

  const meta = [];
  if (item.title !== item.source_title) meta.push(item.source_title);
  meta.push(fmtDuration(item.duration), item.mode === "audio" ? "音訊" : "影片", LANGUAGE_NAMES[item.language]);

  let badge = null;
  if (job) {
    const pct = job.progress != null ? ` ${Math.round(job.progress * 100)}%` : "";
    const label = job.status === "queued" ? "排隊中" : job.cancelling ? "取消中" : `${job.stage || "處理中"}${pct}`;
    badge = el("span", { class: "badge" }, label);
  }

  const row = el("article", { class: "song tree-row" + (state.selected.has(item.key) ? " picked" : ""), style: `--depth:${depth}` },
    el("svg", { class: "grip", "aria-hidden": "true" }, el("use", { href: "#i-grip" })),
    pickBox([item], "勾選（批次操作）；按住 Shift 可連續勾選"),
    el("div", { class: "song-main" },
      el("div", { class: "song-top" },
        el("div", { class: "grow" },
          el("div", { class: "title-line" },
            el("div", {
              class: "song-title ellipsis",
              title: `${item.title}\n輸出：export/${item.export_name}${item.exported ? "" : "（伴唱帶完成後輸出）"}`,
            }, item.title,
              item.artist ? el("span", { class: "artist" }, item.artist) : null),
            el("button", {
              type: "button", class: "edit-btn", title: "編輯歌手與歌名", "aria-label": "編輯歌手與歌名",
              onclick: (e) => startInlineEdit(e.currentTarget.parentElement, row, {
                value: item.artist ? `${item.artist} - ${item.title}` : item.title,
                placeholder: "歌手 - 歌名",
                hint: "Enter 儲存 · Esc 取消 · 清空還原自動",
                onSave: (value) => saveSongName(item, value),
              }),
            }, icon("edit"))),
          el("div", { class: "song-meta ellipsis", title: meta.filter(Boolean).join(" · ") }, meta.filter(Boolean).join(" · "))),
        badge),
      el("div", { class: "song-bottom" }, el("div", { class: "stages-wrap" }, stages, approvalChip(item), qaChip(item)),
        el("div", { class: "actions" }, run, run.length ? el("span", { class: "divider" }) : null, view))),
    renderPreview(item));
  makeDraggable(row, { type: "song", id: item.key, parent: item.folder });
  // 歌曲拖到歌曲上：插在它前面或後面；資料夾拖到歌曲上：放進那首歌所在的資料夾。
  makeDropTarget(row, { kind: "song", id: item.key, parent: item.folder });
  return row;
}

// ---- 勾選與批次操作 ---------------------------------------------------------

/** 依畫面上的順序列出所有歌曲（含收合資料夾裡的）。 */
function songsInOrder(parent = null) {
  const out = [];
  for (const folder of state.folders.filter((f) => f.parent === parent)) out.push(...songsInOrder(folder.id));
  out.push(...state.items.filter((i) => i.folder === parent));
  return out;
}

/** 資料夾裡（含所有子資料夾）的歌曲。 */
function songsUnder(folderId) {
  return state.items.filter((i) => i.folder === folderId).concat(
    ...state.folders.filter((f) => f.parent === folderId).map((f) => songsUnder(f.id)));
}

/** 勾選框：songs 全部勾選時打勾、部分勾選時顯示半勾；點了全部勾選或全部取消。 */
function pickBox(songs, title) {
  const picked = songs.filter((i) => state.selected.has(i.key)).length;
  const box = el("input", { type: "checkbox", class: "pick", title, "aria-label": title, disabled: !songs.length });
  box.checked = songs.length > 0 && picked === songs.length;
  box.indeterminate = picked > 0 && picked < songs.length;
  box.addEventListener("click", (e) => {
    e.stopPropagation();
    pickSongs(songs, box.checked, e.shiftKey);
  });
  for (const type of ["mousedown", "dblclick"]) box.addEventListener(type, (e) => e.stopPropagation());
  return box;
}

function pickSongs(songs, on, shift = false) {
  let targets = songs;
  // Shift + 勾一首歌：從上一次勾的那首到這首之間全部一起。
  if (shift && songs.length === 1 && state.lastPick) {
    const order = songsInOrder();
    const a = order.findIndex((i) => i.key === state.lastPick);
    const b = order.findIndex((i) => i.key === songs[0].key);
    if (a >= 0 && b >= 0) targets = order.slice(Math.min(a, b), Math.max(a, b) + 1);
  }
  for (const i of targets) if (on) state.selected.add(i.key); else state.selected.delete(i.key);
  if (songs.length === 1) state.lastPick = songs[0].key;
  rerender();
}

/** 勾選的歌，依畫面上的順序。 */
function pickedSongs() {
  return songsInOrder().filter((i) => state.selected.has(i.key));
}

function renderBatchBar() {
  const all = state.items;
  const picked = all.filter((i) => state.selected.has(i.key)).length;
  const head = $("#pick-all");
  head.disabled = !all.length;
  head.checked = all.length > 0 && picked === all.length;
  head.indeterminate = picked > 0 && picked < all.length;
  $("#batch").hidden = !picked;
  $("#batch-count").textContent = `已選 ${picked} 首`;
}

const hasLyrics = (i) => i.stages.lyrics === "done";
const hasTiming = (i) => ["done", "outdated"].includes(i.stages.karaoke);

/**
 * 批次動作。一般動作只處理還沒做完的（done 成立就略過）；重新處理的動作帶 force / realign 強制重做。
 * need 不成立的歌不能做（例如缺歌詞），計入略過的原因。
 */
const BATCH = {
  separate: { steps: ["separate"], done: (i) => i.stages.separate === "done" },
  karaoke: { steps: ["karaoke"], need: [hasLyrics, "缺歌詞"], done: (i) => i.stages.karaoke === "done" },
  check: { steps: ["check"], need: [hasTiming, "還沒有伴唱帶"], done: (i) => i.qa.checked },
  redoSeparate: { steps: ["separate"], extra: { force: true } },
  realign: { steps: ["karaoke"], extra: { realign: true }, need: [hasLyrics, "缺歌詞"] },
  recheck: { steps: ["check"], extra: { force: true }, need: [hasTiming, "還沒有伴唱帶"] },
  redoAll: { steps: ["separate", "karaoke"], extra: { force: true } },
};

async function runBatch(name) {
  const action = BATCH[name];
  const songs = pickedSongs();
  const skipped = {};
  const todo = [];
  for (const item of songs) {
    const reason = item.job ? "處理中"
      : action.need && !action.need[0](item) ? action.need[1]
      : action.done && action.done(item) ? "已完成" : null;
    if (reason) skipped[reason] = (skipped[reason] || 0) + 1; else todo.push(item);
  }
  if (name === "redoAll" && todo.length &&
      !confirm(`全部重做 ${todo.length} 首？\n手動修改過的字幕檔（.ass）會被覆蓋。`)) return;
  let queued = 0;
  let firstJob = null;
  const errors = [];
  for (const item of todo) {
    try {
      const job = await api("/api/jobs", { method: "POST", body: { steps: action.steps, item: item.name, ...action.extra } });
      firstJob ??= job.id;
      queued += 1;
    } catch (e) {
      errors.push(`${item.title}：${e.message}`);
    }
  }
  if (firstJob) selectJob(firstJob);
  const notes = Object.entries(skipped).map(([reason, n]) => `${reason} ${n} 首`).join("、");
  const label = action.steps.map((s) => STEP_NAMES[s]).join(" → ");
  let message = queued ? `已排入 ${queued} 首：${label}` + (notes ? `（略過：${notes}）` : "")
    : `沒有需要處理的歌（${notes}）`;
  if (errors.length) message += `；${errors.length} 首失敗：${errors[0]}`;
  toast(message, errors.length > 0);
  await loadState();
}

async function batchApproval(approved) {
  const songs = pickedSongs();
  const todo = approved ? songs.filter((i) => i.stages.karaoke === "done" && i.approval !== "approved")
    : songs.filter((i) => i.approval);
  const skipped = songs.length - todo.length;
  let done = 0;
  for (const item of todo) {
    try {
      await setApproval(item, approved);
      done += 1;
    } catch (e) {
      toast(`${item.title}：${e.message}`, true);
    }
  }
  const why = approved ? "已確認或伴唱帶還沒做好" : "原本就沒確認";
  toast(`${approved ? "已標記確認" : "已取消確認"} ${done} 首` + (skipped ? `（略過 ${skipped} 首：${why}）` : ""));
  await refresh();
}

$("#batch-approve").addEventListener("click", () => batchApproval(true));
$("#batch-unapprove").addEventListener("click", () => batchApproval(false));
$("#pick-all").addEventListener("click", (e) => pickSongs(state.items, e.currentTarget.checked));
$("#batch-clear").addEventListener("click", () => { state.selected.clear(); rerender(); });
for (const button of document.querySelectorAll("[data-batch]")) {
  button.addEventListener("click", () => runBatch(button.dataset.batch));
}
$("#batch-move").addEventListener("click", (e) => {
  const keys = pickedSongs().map((i) => i.key);
  const current = new Set(pickedSongs().map((i) => i.folder));
  const here = (id) => current.size === 1 && current.has(id);
  const entries = [
    menuEntry("曲庫最上層", here(null) ? "都已經在這裡" : "", () => moveGroup(keys, { mode: "into", folder: null }),
      { disabled: here(null), iconName: "folder" }),
  ];
  const walk = (parent, depth) => {
    for (const f of state.folders.filter((x) => x.parent === parent)) {
      const entry = menuEntry(f.name, here(f.id) ? "都已經在這裡" : "", () => moveGroup(keys, { mode: "into", folder: f.id }),
        { disabled: here(f.id), iconName: "folder" });
      entry.style.paddingLeft = `${10 + depth * 18}px`;
      entries.push(entry);
      walk(f.id, depth + 1);
    }
  };
  walk(null, 1);
  showMenu(e.currentTarget, `把 ${keys.length} 首移到…`, entries);
});
$("#batch-redo").addEventListener("click", (e) => {
  showMenu(e.currentTarget, "重新處理選取的歌（已完成的也會重做）", [
    menuEntry("重新去人聲", "重新分離人聲與伴奏；之後伴唱帶會顯示需更新", () => runBatch("redoSeparate")),
    menuEntry("重新對時", "歌詞不變、重新抓每個字的時間並重新燒錄（缺歌詞的略過）", () => runBatch("realign")),
    menuEntry("重新檢查對時", "已經檢查過的也重新聽寫比對（沒有伴唱帶的略過）", () => runBatch("recheck")),
    menuEntry("全部重做", "去人聲、對時、字幕、燒錄全部重來；會覆蓋手動修改過的字幕檔",
      () => runBatch("redoAll"), { danger: true }),
  ]);
});


// ---- 原位編輯 ---------------------------------------------------------------

/**
 * 把 container 的內容換成輸入框（預設填入目前的值並全選，方便直接複製）。
 * Enter 或點別處儲存、Esc 取消；值沒變就不儲存。編輯期間暫停自動重繪與拖曳。
 */
function startInlineEdit(container, row, { value, placeholder, hint = null, onSave }) {
  // 另一個編輯框還開著（例如沒觸發到點別處）：先把它儲存並關掉。
  if (state.finishEdit) state.finishEdit(true);
  state.editing = true;
  row.draggable = false;
  const input = el("input", { class: "inline-edit", value, placeholder, spellcheck: "false", "aria-label": placeholder });
  container.replaceChildren(input, hint ? el("span", { class: "inline-edit-hint" }, hint) : null);

  let done = false;
  const finish = async (commit) => {
    if (done) return;
    done = true;
    const next = input.value.trim();
    if (state.finishEdit === finish) {
      state.finishEdit = null;
      state.editing = false;
    }
    // 立刻換回文字；若同時開了別的編輯框，完整的重繪會延後，不能讓舊輸入框留在畫面上。
    const shown = commit && next !== value.trim() ? (next || "（還原自動辨識）") : value;
    container.replaceChildren(el("span", { class: "edit-pending ellipsis" }, shown));
    try {
      if (commit && next !== value.trim()) {
        await onSave(next);
        toast("已更新");
      }
    } catch (e) {
      toast(e.message, true);
    } finally {
      state.renderKey = "";
      await refresh();   // 若這時已經開了新的編輯框，重繪會自動延後
    }
  };
  state.finishEdit = finish;
  input.addEventListener("keydown", (e) => {
    if (e.key === "Enter") { e.preventDefault(); finish(true); }
    if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); finish(false); }
  });
  input.addEventListener("blur", () => finish(true));
  // 輸入框裡的點擊、選字不要觸發列本身的選取或拖曳。
  for (const type of ["click", "mousedown", "dblclick"]) input.addEventListener(type, (e) => e.stopPropagation());
  input.focus();
  input.select();
}

/** 「歌手 - 歌名」拆成兩欄存成手動設定；沒有「 - 」時只改歌名；清空則還原自動辨識。 */
function saveSongName(item, value) {
  let title = value;
  let artist = null;   // null = 不改
  if (!value) {
    title = "";
    artist = "";
  } else {
    const cut = value.indexOf(" - ");
    if (cut > 0) {
      artist = value.slice(0, cut).trim();
      title = value.slice(cut + 3).trim();
    }
  }
  return api(`/api/songs/${enc(item.key)}`, { method: "PUT", body: { folder: item.folder, title, artist } });
}

// ---- 拖曳 -------------------------------------------------------------------

function makeDraggable(node, payload) {
  node.draggable = true;
  node.addEventListener("dragstart", (e) => {
    if (e.target.closest("button, input, a")) return;  // 從按鈕開始拖不算
    const group = payload.type === "song" && state.selected.has(payload.id) && state.selected.size > 1
      ? pickedSongs() : null;
    state.drag = group ? { ...payload, group: group.map((i) => i.key), parents: new Set(group.map((i) => i.folder)) } : payload;
    e.dataTransfer.effectAllowed = "move";
    e.dataTransfer.setData("text/plain", payload.id);
    node.classList.add("dragging-source");
    if (group) {
      for (const n of document.querySelectorAll(".song.picked")) n.classList.add("dragging-source");
      const ghost = el("div", { class: "drag-ghost" }, `${group.length} 首歌`);
      document.body.append(ghost);
      e.dataTransfer.setDragImage(ghost, 12, 12);
      setTimeout(() => ghost.remove());
    }
    document.body.classList.add("is-dragging");
  });
  node.addEventListener("dragend", () => endDrag());
}

const DROP_CLASSES = ["drop-target", "drop-before", "drop-after"];

function endDrag() {
  state.drag = null;
  clearTimeout(state.expandTimer);
  document.body.classList.remove("is-dragging");
  for (const n of document.querySelectorAll(".dragging-source, .drop-target, .drop-before, .drop-after")) {
    n.classList.remove("dragging-source", ...DROP_CLASSES);
  }
}

/**
 * 依滑鼠在目標列上的位置決定放下的效果：
 *   into   放進資料夾（拖到資料夾中段、或拖到「曲庫」）
 *   before / after  在同一種項目之間插入（歌曲拖到歌曲的上 / 下半部，資料夾拖到資料夾的上 / 下緣）
 * 回傳 null 表示這裡不能放。
 */
function dropZone(e, node, info) {
  const drag = state.drag;
  if (!drag) return null;
  let zone;
  if (info.kind === "root") {
    zone = { mode: "into", folder: null };
  } else {
    const r = node.getBoundingClientRect();
    const y = (e.clientY - r.top) / r.height;
    if (drag.type === "song") {
      zone = info.kind === "song"
        ? { mode: y < 0.5 ? "before" : "after", parent: info.parent, ref: info.id }
        : { mode: "into", folder: info.id };
    } else if (info.kind === "folder") {
      zone = y < 0.25 ? { mode: "before", parent: info.parent, ref: info.id }
        : y > 0.75 ? { mode: "after", parent: info.parent, ref: info.id }
          : { mode: "into", folder: info.id };
    } else {
      zone = { mode: "into", folder: info.parent };  // 資料夾拖到歌曲上：放進那首歌所在的資料夾
    }
  }
  return validZone(drag, zone) ? zone : null;
}

function validZone(drag, zone) {
  const parent = zone.mode === "into" ? zone.folder : zone.parent;
  if (drag.group) {
    // 多首一起：全部已經在這一層才不能放；插入位置不能是其中一首。
    if (zone.mode === "into") return !(drag.parents.size === 1 && drag.parents.has(parent));
    return !drag.group.includes(zone.ref);
  }
  if (zone.mode === "into" && parent === drag.parent) return false;  // 已經在這一層
  if (zone.mode !== "into" && zone.ref === drag.id) return false;
  if (drag.type === "folder") {
    if (parent === drag.id) return false;
    if (parent && folderById(parent)?.path.includes(drag.id)) return false;  // 不能移進自己的子資料夾
  }
  return true;
}

function makeDropTarget(node, info) {
  node.addEventListener("dragover", (e) => {
    const zone = dropZone(e, node, info);
    if (!zone) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = "move";
    const cls = zone.mode === "into" ? "drop-target" : `drop-${zone.mode}`;
    if (!node.classList.contains(cls)) {
      for (const n of document.querySelectorAll(".drop-target, .drop-before, .drop-after")) n.classList.remove(...DROP_CLASSES);
      node.classList.add(cls);
    }
    // 停在收合的資料夾中段一下就自動展開，方便拖進更深的層級。
    const folderId = zone.mode === "into" ? zone.folder : null;
    if (folderId && info.kind === "folder" && state.collapsed.has(folderId)) {
      if (state.expandFor !== folderId) {
        clearTimeout(state.expandTimer);
        state.expandFor = folderId;
        state.expandTimer = setTimeout(() => {
          state.collapsed.delete(folderId);
          remember();
          const drag = state.drag;
          renderLibrary();
          state.drag = drag;  // 重繪後原本的拖曳仍然有效
        }, 700);
      }
    } else {
      clearTimeout(state.expandTimer);
      state.expandFor = null;
    }
  });
  node.addEventListener("dragleave", (e) => {
    if (!node.contains(e.relatedTarget)) node.classList.remove(...DROP_CLASSES);
  });
  node.addEventListener("drop", async (e) => {
    e.preventDefault();
    e.stopPropagation();
    const zone = dropZone(e, node, info);
    const drag = state.drag;
    endDrag();
    if (drag && zone) await applyDrop(drag, zone);
  });
}

/** 同一層、同一種項目（依編號排序），不含正在拖的那一個。 */
function siblingsOf(kind, parent, exclude) {
  const list = kind === "folder"
    ? state.folders.filter((f) => f.parent === parent).map((f) => ({ id: f.id, order: f.order }))
    : state.items.filter((i) => i.folder === parent).map((i) => ({ id: i.key, order: i.order }));
  return list.filter((x) => x.id !== exclude).sort((a, b) => a.order - b.order);
}

async function applyDrop(drag, zone) {
  if (drag.group) return moveGroup(drag.group, zone);
  try {
    if (zone.mode === "into") {
      if (drag.type === "folder") {
        await api(`/api/folders/${drag.id}`, { method: "PATCH", body: { parent: zone.folder, move: true } });
      } else {
        await api(`/api/songs/${enc(drag.id)}`, { method: "PUT", body: { folder: zone.folder } });
      }
      if (zone.folder) state.collapsed.delete(zone.folder);
      remember();
      toast(`已移到 ${zone.folder ? folderById(zone.folder).path_label : "曲庫最上層"}`);
    } else {
      // after = 排在下一個同層項目前面；沒有下一個就排到最後。
      const siblings = siblingsOf(drag.type, zone.parent, drag.id);
      const index = siblings.findIndex((s) => s.id === zone.ref);
      const before = zone.mode === "before" ? zone.ref : siblings[index + 1]?.id ?? null;
      await api("/api/place", { method: "POST", body: { kind: drag.type, id: drag.id, parent: zone.parent, before } });
      toast("已調整順序");
    }
    state.renderKey = "";
    await refresh();
  } catch (e) {
    toast(e.message, true);
  }
}

/** 多首歌一起移動（批次列的「移動到」或拖曳已勾選的歌），彼此的先後順序不變。 */
async function moveGroup(keys, zone) {
  const body = { keys, folder: zone.mode === "into" ? zone.folder : zone.parent };
  if (zone.mode !== "into") {
    const siblings = siblingsOf("song", zone.parent, null).filter((x) => !keys.includes(x.id));
    const index = siblings.findIndex((x) => x.id === zone.ref);
    body.before = zone.mode === "before" ? zone.ref : siblings[index + 1]?.id ?? null;
    body.keep_present = false;
  }
  try {
    const { moved } = await api("/api/songs/move", { method: "POST", body });
    if (body.folder) state.collapsed.delete(body.folder);
    remember();
    const where = body.folder ? folderById(body.folder).path_label : "曲庫最上層";
    toast(zone.mode === "into"
      ? (moved ? `已把 ${moved} 首移到 ${where}` + (moved < keys.length ? `（${keys.length - moved} 首原本就在這裡）` : "") : `選取的歌都已經在 ${where}`)
      : `已移動 ${moved} 首`);
    state.renderKey = "";
    await refresh();
  } catch (e) {
    toast(e.message, true);
  }
}

makeDropTarget($("#lib-root"), { kind: "root" });
$("#lib-root").addEventListener("click", () => selectFolder(null));
$("#expand-all").addEventListener("click", () => { state.collapsed.clear(); remember(); rerender(); });
$("#collapse-all").addEventListener("click", () => {
  state.collapsed = new Set(state.folders.map((f) => f.id));
  remember();
  rerender();
});

/** 手動確認過成品沒問題：已確認（綠）/ 確認後成品變了，需重新確認（橘）。點了開啟播放畫面。 */
function approvalChip(item) {
  if (!item.approval) return null;
  const ok = item.approval === "approved";
  const when = item.approved_at ? item.approved_at.slice(0, 16).replace("T", " ") : "";
  return el("button", {
    type: "button", class: "approval-chip" + (ok ? "" : " stale"),
    title: ok ? `已確認成品沒問題（${when}）` : `確認後成品有變動（重新製作、調整時間或改歌詞），請重新看過再確認`,
    onclick: () => openStudio(item),
  }, icon("check"), ok ? "已確認" : "需重新確認");
}

async function setApproval(item, approved) {
  await api(`/api/items/${enc(item.name)}/approval`, { method: "PUT", body: { approved } });
}

/** 對時檢查有標出句子時，在狀態旁顯示提醒；點了直接從第一個有疑慮的句子開始播放。 */
function qaChip(item) {
  const { checked, wrong, suspect } = item.qa || {};
  if (!checked || !(wrong || suspect)) return null;
  const text = wrong ? `${wrong} 句可能不準` : `${suspect} 句待確認`;
  const tip = [wrong ? `${wrong} 句與獨立聽寫差 1.5 秒以上` : null, suspect ? `${suspect} 句待確認` : null]
    .filter(Boolean).join("、") + "（點一下從第一句開始播放確認）";
  return el("button", {
    type: "button", class: "qa-chip" + (wrong ? "" : " soft"), title: tip,
    onclick: () => openStudio(item, { jumpToQa: true }),
  }, icon("warn"), text);
}

// ---- 預覽按鈕 ---------------------------------------------------------------

/** 每首歌最右邊的主要按鈕：按下預覽最終輸出（沒有的話預覽目前最完整的版本），箭頭選其他版本。 */
function renderPreview(item) {
  const main = item.media[0];
  return el("div", { class: "preview" },
    el("div", { class: "preview-split" },
      el("button", {
        type: "button", class: "preview-main", disabled: !main,
        title: main ? `播放${main.label}，同時預覽 / 編輯歌詞` : "還沒有可播放的影片",
        "aria-label": main ? `播放${main.label}` : "播放",
        onclick: () => openStudio(item),
      }, icon("play")),
      el("button", {
        type: "button", class: "preview-more", disabled: item.media.length < 2,
        title: "播放其他版本", "aria-label": "播放其他版本", "aria-haspopup": "menu",
        onclick: (e) => openPreviewMenu(e.currentTarget, item),
      }, icon("caret"))),
    el("span", { class: "preview-label" }, main ? main.label : "尚無影片"));
}

function openPreviewMenu(anchor, item) {
  showMenu(anchor, "播放其他版本", item.media.map((media, i) =>
    menuEntry(media.label + (i === 0 ? "（預設）" : ""), media.desc, () => openStudio(item, { key: media.key }),
      { iconName: "play" })));
}

// ---- 選單 -------------------------------------------------------------------

function menuEntry(label, desc, onclick, { disabled = false, danger = false, iconName = "play" } = {}) {
  return el("button", {
    type: "button", role: "menuitem", class: "menu-item" + (danger ? " danger" : ""), disabled,
    onclick: () => { closeMenu(); onclick(); },
  }, icon(iconName), el("span", { class: "label" }, label), el("span", { class: "desc" }, desc));
}

function showMenu(anchor, title, entries) {
  const menu = $("#menu");
  menu.replaceChildren(el("div", { class: "menu-title" }, title), ...entries);
  menu.hidden = false;
  const rect = anchor.getBoundingClientRect();
  const left = Math.min(rect.right - menu.offsetWidth, window.innerWidth - menu.offsetWidth - 8);
  const below = rect.bottom + 6 + menu.offsetHeight < window.innerHeight;
  menu.style.left = `${Math.max(8, left)}px`;
  menu.style.top = `${below ? rect.bottom + 6 : rect.top - menu.offsetHeight - 6}px`;
  state.menuAnchor = anchor;
}

function openRedoMenu(anchor, item) {
  const busy = Boolean(item.job);
  const hasLyrics = item.stages.lyrics === "done";
  showMenu(anchor, busy ? "處理中，請等目前的工作結束" : "重新處理（會排入佇列）", [
    menuEntry("重新去人聲", "重新分離人聲與伴奏；之後伴唱帶會顯示需更新",
      () => runSteps(item, ["separate"], { force: true }), { disabled: busy }),
    menuEntry("重新對時", "歌詞不變、重新抓每個字的時間，並重新燒錄",
      () => runSteps(item, ["karaoke"], { realign: true }), { disabled: busy || !hasLyrics }),
    menuEntry("檢查對時", "用本機 Whisper 獨立聽寫，標出可能不準的句子（約半分鐘）",
      () => runSteps(item, ["check"], { force: true }),
      { disabled: busy || !["done", "outdated"].includes(item.stages.karaoke) }),
    menuEntry("全部重做", "去人聲、對時、字幕、燒錄全部重來；會覆蓋手動修改過的字幕檔", () => {
      if (confirm(`全部重做「${item.title}」？\n手動修改過的字幕檔（.ass）會被覆蓋。`)) {
        runSteps(item, ["separate", "karaoke"], { force: true });
      }
    }, { disabled: busy, danger: true }),
  ]);
}

function closeMenu() {
  $("#menu").hidden = true;
  state.menuAnchor = null;
}

document.addEventListener("mousedown", (e) => {
  if (!$("#menu").hidden && !$("#menu").contains(e.target) && !state.menuAnchor?.contains(e.target)) closeMenu();
});
window.addEventListener("scroll", closeMenu, true);

async function runSteps(item, steps, extra = {}) {
  try {
    const job = await api("/api/jobs", { method: "POST", body: { steps, item: item.name, ...extra } });
    selectJob(job.id);
    const mode = steps.includes("check") ? "" : extra.force ? "（強制重做）" : extra.realign ? "（重新對時）" : "";
    toast(`已排入佇列：${steps.map((s) => STEP_NAMES[s]).join(" → ")}${mode}`);
    await loadState();
  } catch (e) {
    toast(e.message, true);
  }
}

async function revealItem(item) {
  const stage = item.stages.karaoke === "done" ? "karaoke" : item.stages.separate === "done" ? "separate" : "download";
  try {
    await api(`/api/items/${enc(item.name)}/open`, { method: "POST", body: { stage } });
  } catch (e) {
    toast(e.message, true);
  }
}

$("#new-folder").addEventListener("click", () => openFolderDialog(null, null));
$("#open-export").addEventListener("click", async () => {
  try { await api("/api/export/open", { method: "POST" }); } catch (e) { toast(e.message, true); }
});

// ---- 新增歌曲 ---------------------------------------------------------------

$("#new-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const steps = (event.submitter?.dataset.steps || "download").split(",");
  const body = {
    steps,
    url: $("#url").value.trim(),
    folder: state.folder,
    audio_only: $("#audio-only").checked,
    lyrics: $("#new-lyrics-text").value.trim() || null,
  };
  try {
    const job = await api("/api/jobs", { method: "POST", body });
    $("#url").value = "";
    $("#new-lyrics-text").value = "";
    $("#new-lyrics").open = false;
    selectJob(job.id);
    toast("已排入佇列");
    await loadState();
  } catch (e) {
    toast(e.message, true);
  }
});

// ---- 資料夾選單 -------------------------------------------------------------

function fillFolderSelect(select, selected, exclude = null) {
  select.replaceChildren(el("option", { value: "" }, "曲庫最上層"));
  for (const folder of state.folders) {
    if (exclude && folder.path.includes(exclude)) continue;  // 不能移到自己或子資料夾裡
    select.append(el("option", { value: folder.id }, folder.path_label));
  }
  select.value = selected || "";
}

// ---- 歌曲資訊 ---------------------------------------------------------------

let songEditing = null;

/** 手動欄位留空時會用的值：歌詞檔的 # title 優先，其次是自動辨識。 */
function fallbackInfo(item) {
  const auto = { metadata: "影片的歌曲資訊", title: "影片標題", fallback: "影片標題（無法辨識格式）" }[item.auto_source];
  const titleFrom = item.lyrics_title ? "歌詞檔" : auto;
  const artistFrom = item.lyrics_artist ? "歌詞檔" : auto;
  return {
    title: item.lyrics_title || item.auto_title,
    artist: item.lyrics_artist || item.auto_artist,
    from: titleFrom === artistFrom ? titleFrom : `歌名來自${titleFrom}、演唱者來自${artistFrom}`,
  };
}

function openSongDialog(item) {
  songEditing = item;
  const fb = fallbackInfo(item);
  fillFolderSelect($("#song-folder"), item.folder);
  $("#song-title").value = item.custom_title;
  $("#song-artist").value = item.custom_artist;
  $("#song-note").value = item.note || "";
  $("#song-translation").checked = item.translation !== false;
  $("#song-translation-hint").textContent = item.translation_lines
    ? `歌詞裡有 ${item.translation_lines} 句翻譯。只顯示正在唱的那一句，放在歌詞上方；切換後只重新燒錄，不會重新對時。`
    : "歌詞還沒有翻譯：在歌詞編輯器每句下面填翻譯（或在每句下一行寫「> 翻譯」）。";
  $("#song-title").placeholder = fb.title;
  $("#song-artist").placeholder = fb.artist || "（未填）";
  const from = fb.from.startsWith("歌名") ? fb.from : `來自${fb.from}`;
  $("#song-hint").textContent = `留空則自動使用「${fb.title}${fb.artist ? ` / ${fb.artist}` : ""}」（${from}）。`;
  $("#song-source").textContent = item.source_title;
  $("#song-language").value = item.language || "";
  updateLanguageHint();
  // 用網址下載的歌：連結唯讀（可以複製）；手動放入的影片：可以補上原始連結，重做時用它重新下載。
  const downloaded = Boolean(item.url);
  $("#song-link").value = item.url || item.link || "";
  $("#song-link").readOnly = downloaded;
  $("#song-link").placeholder = downloaded ? "" : "https://www.youtube.com/watch?v=…";
  $("#song-link-hint").textContent = downloaded
    ? "這首歌是從這個連結下載的（唯讀）。"
    : "手動放入的影片：可以補上原始影片的連結，從資料備份重做時會用它重新下載（換了來源會重新對時）。";
  updateLinkCopy();
  updateSongPreview();
  $("#song-dialog").hidden = false;
  $("#song-title").focus();
}

function updateSongPreview() {
  const item = songEditing;
  if (!item) return;
  const fb = fallbackInfo(item);
  const folder = folderById($("#song-folder").value || null);
  const title = $("#song-title").value.trim() || fb.title;
  const artist = $("#song-artist").value.trim() || fb.artist;
  const dir = folder ? `${folder.path_label.replaceAll(" › ", "/")}/` : "";
  $("#song-export").textContent = `export/${dir}${exportName(title, artist)}`;
}

function updateLanguageHint() {
  const value = $("#song-language").value;
  const changed = songEditing && value !== (songEditing.language || "") && songEditing.stages.karaoke !== "pending"
    && songEditing.stages.karaoke !== "no_lyrics";
  $("#song-language-hint").textContent = (LANGUAGE_HINTS[value] || "")
    + (changed ? " 改了語言，伴唱帶會顯示需更新（會整首重新對時）。" : "");
}

$("#song-language").addEventListener("change", updateLanguageHint);

function updateLinkCopy() {
  $("#song-link-copy").disabled = !$("#song-link").value.trim();
}

$("#song-link").addEventListener("input", updateLinkCopy);
$("#song-link-copy").addEventListener("click", async () => {
  const input = $("#song-link");
  const text = input.value.trim();
  try {
    await navigator.clipboard.writeText(text);   // 只有本機或 https 才能用
  } catch {
    // 區域網路（http）時退回舊做法：選取後複製
    const readOnly = input.readOnly;
    input.readOnly = false;
    input.select();
    document.execCommand("copy");
    input.readOnly = readOnly;
    input.setSelectionRange(0, 0);
  }
  toast("已複製連結");
});
$("#song-folder").addEventListener("change", updateSongPreview);
for (const id of ["#song-title", "#song-artist"]) $(id).addEventListener("input", updateSongPreview);

$("#song-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const item = songEditing;
  const body = {
    folder: $("#song-folder").value || null,
    title: $("#song-title").value.trim(),
    artist: $("#song-artist").value.trim(),
    language: $("#song-language").value,
    note: $("#song-note").value.trim(),
    translation: $("#song-translation").checked,
  };
  if (!item.url) body.link = $("#song-link").value.trim();   // 只有手動放入的影片可以改連結
  try {
    await api(`/api/songs/${enc(item.key)}`, { method: "PUT", body });
    closeDialog("#song-dialog");
    toast("已更新歌曲資訊");
    await refresh();
  } catch (e) {
    toast(e.message, true);
  }
});

// ---- 全域設定 ---------------------------------------------------------------

const BASE_RATIO = 0.075;   // 預設字幕字高 / 畫面高（和 songtool/ass.py 的 Style 一致）

function openSettings() {
  $("#set-scale").value = Math.round((state.settings?.subtitle_scale ?? 1) * 100);
  $("#settings-dialog").hidden = false;   // 先顯示，預覽框才量得到高度
  updateScalePreview();
}

function updateScalePreview() {
  const scale = Number($("#set-scale").value) / 100;
  $("#set-scale-value").textContent = `${Math.round(scale * 100)}%`;
  // 預覽框和影片同樣是 16:9，字高 = 框高 × 比例（和燒進影片的一樣）
  const box = document.querySelector(".size-preview");
  box.style.setProperty("--fs", `${box.clientHeight * BASE_RATIO * scale}px`);
  const made = state.items.filter((i) => i.stages.karaoke === "done").length;
  const changed = Math.abs(scale - (state.settings?.subtitle_scale ?? 1)) > 0.001;
  $("#set-scale-hint").textContent = "歌詞、假名、翻譯與開頭標題畫面會一起縮放；一行放不下時會自動拆成兩行。"
    + (changed && made ? `儲存後已做好的 ${made} 首伴唱帶會顯示需更新（重新燒錄，不會重新對時），可以用批次「製作伴唱帶」一次更新。` : "");
}

$("#open-settings").addEventListener("click", openSettings);
$("#set-scale").addEventListener("input", updateScalePreview);
$("#set-reset").addEventListener("click", () => { $("#set-scale").value = 100; updateScalePreview(); });
$("#settings-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  try {
    await api("/api/settings", { method: "PUT", body: { subtitle_scale: Number($("#set-scale").value) / 100 } });
    closeDialog("#settings-dialog");
    toast("已儲存設定");
    await refresh();
  } catch (e) {
    toast(e.message, true);
  }
});

// ---- 資料夾 -----------------------------------------------------------------

let folderEditing = null;   // null = 新增

/** folder 為 null 時新增資料夾，放在 parent 底下（null = 最上層）。 */
function openFolderDialog(folder, parent = null) {
  folderEditing = folder;
  const where = folder ? folder.parent : parent;
  $("#folder-heading").textContent = folder ? "編輯資料夾" : "新增資料夾";
  $("#folder-name").value = folder ? folder.name : "";
  fillFolderSelect($("#folder-parent"), where, folder ? folder.id : null);
  $("#folder-delete").hidden = !folder;
  $("#folder-hint").textContent = "資料夾只影響曲庫整理和輸出資料夾，不會搬動下載、去人聲等處理用的檔案。"
    + "也可以直接拖曳資料夾到其他位置，或拖曳調整順序。";
  $("#folder-dialog").hidden = false;
  $("#folder-name").focus();
}

$("#folder-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const body = {
    name: $("#folder-name").value.trim(),
    parent: $("#folder-parent").value || null,
  };
  try {
    if (folderEditing) {
      await api(`/api/folders/${folderEditing.id}`, { method: "PATCH", body: { ...body, move: true } });
      toast("已更新資料夾");
    } else {
      await api("/api/folders", { method: "POST", body });
      if (body.parent) state.collapsed.delete(body.parent);  // 讓新資料夾看得到
      remember();
      toast("已新增資料夾");
    }
    closeDialog("#folder-dialog");
    await refresh();
  } catch (e) {
    toast(e.message, true);
  }
});

$("#folder-delete").addEventListener("click", async () => {
  const folder = folderEditing;
  if (!confirm(`刪除資料夾「${folder.label}」？\n裡面的歌曲與子資料夾會移到上一層，不會刪除任何檔案。`)) return;
  try {
    await api(`/api/folders/${folder.id}`, { method: "DELETE" });
    closeDialog("#folder-dialog");
    if (state.folder && folderById(state.folder)?.path.includes(folder.id)) {
      state.folder = folder.parent;
      remember();
    }
    toast("已刪除資料夾");
    await refresh();
  } catch (e) {
    toast(e.message, true);
  }
});

// ---- 處理佇列與紀錄 ---------------------------------------------------------

function renderJobs() {
  const box = $("#jobs");
  box.replaceChildren();
  if (!state.jobs.length) {
    box.append(el("div", { class: "empty" }, "目前沒有排隊中的處理"));
    return;
  }
  // 兩條佇列各自獨立：下載不用等 AI 處理。
  const lanes = [["download", "下載", "只用網路，最多同時 2 首"], ["process", "AI 處理", "去人聲、對時、燒錄；一次一首"],
    ["system", "收尾", "佇列清空或曲庫有變動後：資料備份（data/）與 hooks/on_idle.ps1"]];
  for (const [lane, name, tip] of lanes) {
    const jobs = state.jobs.filter((j) => (j.lane || "process") === lane);
    if (lane === "system" && !jobs.length) continue;
    const active = jobs.filter((j) => j.status === "queued" || j.status === "running").length;
    box.append(el("div", { class: "lane-head", title: tip }, name,
      el("span", { class: "muted" }, active ? `${active} 件進行中` : "閒置")));
    for (const job of jobs) box.append(renderJob(job));
  }
}

function renderJob(job) {
  const steps = job.steps.map((s) => STEP_NAMES[s]).join(" → ");
  const mode = job.force ? "（強制重做）" : job.realign ? "（重新對時）" : "";
  const sub = {
    queued: job.lane === "process" && job.steps.includes("download") ? "已下載，等待 AI 處理" : "排隊中",
    running: job.cancelling ? "取消中…" : `${job.stage || "處理中"}…`,
    done: "完成",
    failed: `失敗：${job.error || ""}`,
    cancelled: "已取消",
  }[job.status];
  let bar = null;
  if (job.status === "running") {
    const width = job.progress != null ? `width:${Math.round(job.progress * 100)}%` : null;
    bar = el("div", { class: "bar" + (job.progress == null ? " indeterminate" : "") }, el("span", { style: width }));
  }
  const cancellable = job.lane !== "system" && (job.status === "queued" || job.status === "running") && !job.cancelling;
  return el("div", {
    role: "button", tabindex: "0",
    class: `job ${job.status}` + (job.id === state.selectedJob ? " selected" : ""),
    onclick: () => selectJob(job.id),
    onkeydown: (e) => { if (e.key === "Enter") selectJob(job.id); },
  },
  el("span", { class: "dot" }),
  el("span", { class: "job-title ellipsis" }, job.title || job.url),
  el("span", { class: "job-time" }, fmtTime(job.created)),
  el("span", { class: "job-sub ellipsis" + (cancellable ? " with-cancel" : "") }, `${steps}${mode} · ${sub}`),
  cancellable ? el("button", {
    type: "button", class: "job-cancel",
    title: job.status === "queued" ? "移出佇列" : "中斷處理（對時進行中時會等對時完成才停下）",
    onclick: (e) => { e.stopPropagation(); cancelJob(job); },
  }, "取消") : null,
  bar);
}

async function cancelJob(job) {
  try {
    await api(`/api/jobs/${job.id}/cancel`, { method: "POST" });
    toast(job.status === "queued" ? "已移出佇列" : "取消中，會在下一個安全點停下");
    await refresh();
  } catch (e) {
    toast(e.message, true);
  }
}

function selectJob(id) {
  if (state.selectedJob === id) return;
  state.selectedJob = id;
  state.logOffset = 0;
  $("#log").replaceChildren();
  state.jobsKey = "";  // 強制重繪以更新選取樣式
  renderJobs();
  pollLog().catch(() => {});
}

function logClass(line) {
  const msg = line.slice(10);  // 去掉「HH:MM:SS  」
  if (/失敗|ERROR|\[x\]|Traceback/.test(msg)) return "l-err";
  if (/^完成$/.test(msg)) return "l-ok";
  return msg.startsWith(" ") ? null : "l-step";
}

async function pollLog() {
  const id = state.selectedJob;
  if (!id) return;
  const job = state.jobs.find((j) => j.id === id);
  if (job && job.log_size === state.logOffset) return;
  // 切換工作與背景輪詢可能同時發出請求；只接受起點仍與目前一致的回應，避免重複。
  const offset = state.logOffset;
  const data = await api(`/api/jobs/${id}/log?offset=${offset}`);
  if (state.selectedJob !== id || state.logOffset !== offset) return;
  const box = $("#log");
  const atBottom = box.scrollHeight - box.scrollTop - box.clientHeight < 40;
  for (const line of data.lines) box.append(el("div", { class: logClass(line) }, line));
  state.logOffset = data.next;
  $("#log-title").textContent = data.job.title || "";
  if (atBottom) box.scrollTop = box.scrollHeight;
}

// ---- 歌詞編輯器 -------------------------------------------------------------

const SINGER_ORDER = [null, "男", "女", "合"];
const HINTS = {
  visual: "點左側標籤切換演唱者（Shift+點：套用到整段）。點漢字上的假名可修改讀音，灰色為自動判斷、藍色為手動指定。"
    + "日文歌的假名與每句的演唱者顏色都會出現在伴唱帶上；讀音也會用於對時。"
    + "時間不對時，點右側的時間可以移動這句；整段偏掉請在播放畫面用「AI 重對這句及之後全部」。"
    + "台語 / 粵語歌（在歌曲「資訊」設定）每個漢字都可以點來標台羅 / 粵拼。",
  annotated: "含標註的完整原文：<code>[男]</code> <code>[女]</code> <code>[合]</code> 標註演唱者，"
    + "每句下一行的 <code>&gt; 翻譯</code> 是中文翻譯，"
    + "每個漢字後面的 <code>{よみ}</code> 是讀音（自動判斷的也列出來，直接改就會變成手動指定；"
    + "也可以用 <code>{原字|よみ}</code>）；<code>#</code> 開頭為註解。歌名與演唱者請在歌曲「資訊」設定。",
  plain: "只有歌詞本身（和 <code>&gt; 翻譯</code> 行），可以直接貼上或修改，一行一句。沒改到的句子會保留原本的演唱者與讀音；"
    + "改過的句子保留演唱者，讀音重新自動判斷。<code>#</code> 開頭為註解。",
};
const TEXT_MODES = new Set(["annotated", "plain"]);

const editor = {
  item: null, mode: "visual", doc: null, dirty: false, ruby: null, qa: new Map(),
  views: { text: "", annotated: "", plain: "" },   // 存檔格式、標註原文、原始歌詞
  paren: [],         // 寫在括號裡的讀音（例如「運命(さだめ)」），可以一鍵轉成讀音標註
  timing: [],        // 對時結果每句的 { index, text, start, end }；還沒對時為空
  retimed: false,    // 這次開啟後調整過時間（要重新製作伴唱帶才會生效）
  time: null,        // 正在調整時間的 { index, target }
};

async function openEditor(item) {
  let data, qaData, timingData;
  try {
    [data, qaData, timingData] = await Promise.all([
      api(`/api/items/${enc(item.name)}/lyrics`),
      api(`/api/items/${enc(item.name)}/qa`).catch(() => ({ lines: [] })),
      api(`/api/items/${enc(item.name)}/timing`).catch(() => ({ lines: [] })),
    ]);
  } catch (e) {
    toast(e.message, true);
    return;
  }
  // 對時檢查的結果依「第幾句歌詞」對應；句子內容改過就不再套用，避免標到別句。
  const qa = new Map(qaData.lines.map((c) => [c.index, c]));
  Object.assign(editor, { item, doc: data.doc, dirty: false, qa, timing: timingData.lines, retimed: false });
  editor.views = { text: data.text, annotated: data.annotated, plain: data.plain };
  editor.paren = data.paren || [];
  renderParenBar();
  $("#editor-song").textContent = item.artist ? `${item.artist} - ${item.title}` : item.title;
  $("#editor-studio").hidden = !item.media.some((m) => m.url);
  const hasLines = data.doc.lines.some((l) => l.kind === "lyric");
  editor.mode = null;
  await setMode(hasLines ? "visual" : "plain", false);
  setStatus(data.exists ? data.path : `新檔案：${data.path}`);
  $("#editor").hidden = false;
  if (!hasLines) $("#editor-text").focus();
}

async function convert(body) {
  const language = editor.item?.language || null;
  const result = await api("/api/lyrics/convert", { method: "POST", body: { ...body, language } });
  editor.doc = result.doc;
  editor.views = { text: result.text, annotated: result.annotated, plain: result.plain };
  editor.paren = result.paren || [];
  renderParenBar();
  return result;
}

/** 歌詞裡有「運命(さだめ)」這種寫在括號裡的讀音時提示；由使用者按一下才轉換。 */
function renderParenBar() {
  const found = editor.paren;
  $("#paren-bar").hidden = !found.length;
  if (!found.length) return;
  const sample = found.slice(0, 4).join("、") + (found.length > 4 ? "…" : "");
  $("#paren-text").textContent = `發現 ${found.length} 處寫在括號裡的讀音：${sample}。`
    + "轉換後括號會從歌詞拿掉、讀音改成標註（對時也會照這個唸法）。";
}

$("#paren-fix").addEventListener("click", async () => {
  try {
    await syncEditor();   // 先收進文字模式裡還沒轉換的修改
    const count = editor.paren.length;
    await convert({ text: editor.views.text, format: "file", paren_to_ruby: true });
    markDirty();
    if (TEXT_MODES.has(editor.mode)) $("#editor-text").value = editor.views[editor.mode];
    else renderLines();
    toast(`已轉換 ${count} 處讀音，記得儲存`);
  } catch (e) {
    toast(e.message, true);
  }
});

/** 把目前畫面上的內容（標註模式的結構，或文字模式裡改過的文字）轉成最新的結構與各種檢視。 */
async function syncEditor() {
  if (TEXT_MODES.has(editor.mode)) {
    await convert({ text: $("#editor-text").value, format: editor.mode, doc: editor.doc });
  } else {
    await convert({ doc: editor.doc });
  }
}

async function setMode(mode, sync = true) {
  closeRuby();
  closeTimePop();
  if (sync && mode !== editor.mode) {
    try {
      await syncEditor();
    } catch (e) {
      toast(e.message, true);
      return;
    }
  }
  editor.mode = mode;
  for (const btn of document.querySelectorAll("#mode-toggle button")) btn.classList.toggle("on", btn.dataset.mode === mode);
  $("#editor-visual").hidden = mode !== "visual";
  $("#editor-text").hidden = !TEXT_MODES.has(mode);
  $("#editor-hint").innerHTML = HINTS[mode];  // 固定字串，不含使用者輸入
  if (TEXT_MODES.has(mode)) $("#editor-text").value = editor.views[mode];
  else renderLines();
}

function renderLines() {
  const box = $("#lines");
  box.replaceChildren();
  renderQaSummary();
  if (!editor.doc.lines.some((l) => l.kind === "lyric")) {
    box.append(el("div", { class: "empty-lyrics" }, "還沒有歌詞。切到「原始歌詞」貼上歌詞，一行一句。"));
    return;
  }
  let no = 0;
  editor.doc.lines.forEach((line, index) => {
    if (line.kind === "blank") return box.append(el("div", { class: "blank-line" }));
    if (line.kind === "comment") return box.append(el("div", { class: "comment-line" }, line.text));
    no += 1;
    const check = qaFor(no - 1, line.text);
    const time = timingFor(no - 1, line.text);
    const text = line.segments.map((seg) => (seg.ruby || seg.slot
      ? el("ruby", {
        class: seg.ruby ? (seg.manual ? "manual" : "auto") : "slot",
        dataset: { line: index, start: seg.start },
        title: seg.ruby ? "點一下修改讀音" : "點一下標讀音（按 Enter 會接著標下一個字）",
        onclick: (e) => openRuby(e.currentTarget, index, seg),
      }, seg.text, el("rt", {}, seg.ruby || "·"))
      : seg.text));
    box.append(el("div", { class: "lyric-line" + (check ? ` qa-${check.status}` : "") },
      el("span", { class: "line-no" }, no),
      el("button", {
        type: "button",
        class: "singer",
        dataset: line.singer ? { singer: line.singer } : {},
        title: "演唱者：點一下切換（Shift+點：套用到整段）",
        onclick: (e) => cycleSinger(index, e.shiftKey),
      }, line.singer || "—"),
      el("div", { class: "line-body" },
        el("div", { class: "line-text" }, text),
        el("input", {
          class: "trans-input", value: line.translation || "", placeholder: "中文翻譯（選填）", spellcheck: "false",
          "aria-label": "中文翻譯",
          oninput: (e) => { editor.doc.lines[index].translation = e.target.value; markDirty(); },
        })),
      check ? el("button", {
        type: "button", class: "qa-flag",
        title: `${check.status === "wrong" ? "可能不準" : "待確認"}：${check.reasons.join("；")}\n點一下跳到這句播放`,
        "aria-label": "播放這句",
        onclick: () => openStudioFromEditor({ line: check.index }),
      }, icon("warn")) : null,
      time ? el("button", {
        type: "button", class: "line-time", title: "調整這句的開始時間（只移這一句）",
        onclick: (e) => openTimePop(e.currentTarget, time),
      }, fmtClock(time.start)) : null));
  });
}

/** 第 index 句目前的時間；歌詞改過（內容對不上）時不顯示，避免調到別句。 */
function timingFor(index, text) {
  const t = editor.timing[index];
  return t && t.text.replace(/\s/g, "") === text.replace(/\s/g, "") ? t : null;
}

function fmtClock(sec) {
  const m = Math.floor(sec / 60);
  return `${m}:${(sec - m * 60).toFixed(2).padStart(5, "0")}`;
}

// ---- 調整時間 ---------------------------------------------------------------

function openTimePop(target, time) {
  closeTimePop();
  closeRuby();
  editor.time = { index: time.index, target };
  target.classList.add("editing");
  const pop = $("#time-pop");
  $("#time-title").textContent = `第 ${time.index + 1} 句：${time.text}`;
  $("#time-delta").value = "0";
  $("#time-warn").hidden = !editor.dirty;
  $("#time-warn").textContent = "歌詞有尚未儲存的修改：儲存後會重新對時，這裡調整的時間會被取代。";
  $("#time-listen").disabled = !listenSource();
  updateTimeNow();
  pop.hidden = false;
  const rect = target.getBoundingClientRect();
  const left = Math.min(Math.max(8, rect.right - pop.offsetWidth), window.innerWidth - pop.offsetWidth - 8);
  const below = rect.bottom + 8 + pop.offsetHeight < window.innerHeight;
  pop.style.left = `${left}px`;
  pop.style.top = `${below ? rect.bottom + 8 : Math.max(8, rect.top - pop.offsetHeight - 8)}px`;
  $("#time-delta").focus();
  $("#time-delta").select();
}

function closeTimePop() {
  const audio = $("#time-audio");
  audio.pause();
  clearTimeout(editor.listenTimer);
  if (editor.time) editor.time.target.classList.remove("editing");
  editor.time = null;
  $("#time-pop").hidden = true;
}

function timeDelta() {
  const value = parseFloat($("#time-delta").value);
  return Number.isFinite(value) ? Math.round(value * 1000) / 1000 : 0;
}

function updateTimeNow() {
  if (!editor.time) return;
  const t = editor.timing[editor.time.index];
  const delta = timeDelta();
  const sign = delta > 0 ? "+" : "";
  $("#time-now").replaceChildren(`開始 ${fmtClock(t.start)}`,
    delta ? el("span", {}, " → ", el("b", {}, fmtClock(Math.max(0, t.start + delta))), `（${sign}${delta} 秒）`) : "");
  $("#time-apply").disabled = !delta;
}

/** 試聽用的音源：優先用人聲（最容易聽出句子從哪裡開始），沒有就用原曲。 */
function listenSource() {
  const media = editor.item.media || [];
  return (media.find((m) => m.key === "vocals" && m.url) || media.find((m) => m.key === "source" && m.url))?.url;
}

for (const btn of document.querySelectorAll("#time-pop .nudge button")) {
  btn.addEventListener("click", () => {
    $("#time-delta").value = String(Math.round((timeDelta() + parseFloat(btn.dataset.d)) * 1000) / 1000);
    updateTimeNow();
  });
}
$("#time-delta").addEventListener("input", updateTimeNow);
$("#time-delta").addEventListener("keydown", (e) => {
  if (e.key === "Enter") { e.preventDefault(); applyTime(); }
});
$("#time-listen").addEventListener("click", () => {
  if (!editor.time) return;
  const audio = $("#time-audio");
  const start = Math.max(0, editor.timing[editor.time.index].start + timeDelta() - 1);
  const url = listenSource();
  clearTimeout(editor.listenTimer);
  const go = () => {
    audio.currentTime = start;
    audio.play().catch(() => {});
    editor.listenTimer = setTimeout(() => audio.pause(), 5000);
  };
  if (!audio.src.endsWith(url)) {
    audio.src = url;
    audio.addEventListener("loadedmetadata", go, { once: true });
    audio.load();
  } else {
    go();
  }
});
$("#time-apply").addEventListener("click", () => applyTime());
document.addEventListener("mousedown", (e) => {
  if (editor.time && !$("#time-pop").contains(e.target) && !editor.time.target.contains(e.target)) closeTimePop();
});

async function applyTime() {
  if (!editor.time) return;
  const delta = timeDelta();
  if (!delta) return;
  const following = false;   // 只移這一句；整段偏掉交給 AI 重對
  try {
    const data = await api(`/api/items/${enc(editor.item.name)}/timing`, {
      method: "POST", body: { line: editor.time.index, delta, following },
    });
    const pushed = data.pushed || [];
    closeTimePop();
    editor.timing = data.lines;
    editor.retimed = true;
    renderLines();
    toast(`已移動 ${delta > 0 ? "+" : ""}${delta} 秒`
      + (pushed.length ? `（第 ${pushed.map((k) => k + 1).join("、")} 句跟著往後挪）` : "")
      + "。按「儲存並製作伴唱帶」重新燒錄後生效");
    refresh();
  } catch (e) {
    toast(e.message, true);
  }
}

/** 第 index 句的對時檢查結果；歌詞改過（內容對不上）時不套用。 */
function qaFor(index, text) {
  const check = editor.qa.get(index);
  return check && check.text.replace(/\s/g, "") === text.replace(/\s/g, "") ? check : null;
}

function renderQaSummary() {
  const box = $("#qa-summary");
  const checks = [...editor.qa.values()];
  const wrong = checks.filter((c) => c.status === "wrong").length;
  const suspect = checks.length - wrong;
  box.hidden = !checks.length;
  if (!checks.length) return;
  box.replaceChildren(icon("warn"), el("span", {},
    `對時檢查：${[wrong ? `${wrong} 句可能不準` : null, suspect ? `${suspect} 句待確認` : null].filter(Boolean).join("、")}。`
    + "點該句右邊的 ⚠ 跳到那句播放確認；時間偏了可以點最右邊的時間調整這句，整段偏掉在播放畫面用「AI 重對這句及之後全部」；讀音或歌詞有誤就修正後重新製作。"));
}

function cycleSinger(index, wholeBlock) {
  const lines = editor.doc.lines;
  const next = SINGER_ORDER[(SINGER_ORDER.indexOf(lines[index].singer ?? null) + 1) % SINGER_ORDER.length];
  lines[index].singer = next;
  // 整段：往下套用到遇到空行或註解為止。
  for (let j = index + 1; wholeBlock && j < lines.length && lines[j].kind === "lyric"; j++) lines[j].singer = next;
  markDirty();
  renderLines();
}

function openRuby(target, lineIndex, seg) {
  closeRuby();
  editor.ruby = { lineIndex, seg, target };
  target.classList.add("editing");
  const pop = $("#ruby-pop");
  $(".pop-base", pop).textContent = seg.text;
  $("#ruby-input").value = seg.ruby || "";
  $("#ruby-reset").disabled = !seg.manual;
  pop.hidden = false;
  const rect = target.getBoundingClientRect();
  const left = Math.min(Math.max(8, rect.left), window.innerWidth - pop.offsetWidth - 8);
  const below = rect.bottom + 8 + pop.offsetHeight < window.innerHeight;
  pop.style.left = `${left}px`;
  pop.style.top = `${below ? rect.bottom + 8 : rect.top - pop.offsetHeight - 8}px`;
  $("#ruby-input").focus();
  $("#ruby-input").select();
}

function closeRuby() {
  if (editor.ruby) editor.ruby.target.classList.remove("editing");
  editor.ruby = null;
  $("#ruby-pop").hidden = true;
}

async function applyRuby(reading, { advance = false } = {}) {
  if (!editor.ruby) return;
  const { lineIndex, seg } = editor.ruby;
  closeRuby();
  const changed = seg.manual || reading !== (seg.ruby || "");  // 和自動判斷相同，不必另外記錄
  if (changed) {
    const line = editor.doc.lines[lineIndex];
    line.rubies = line.rubies.filter((r) => r.end <= seg.start || r.start >= seg.end);
    if (reading) line.rubies.push({ start: seg.start, end: seg.end, reading });
    try {
      await convert({ doc: editor.doc });  // 重新計算假名片段
      markDirty();
      renderLines();
    } catch (e) {
      toast(e.message, true);
      return;
    }
  }
  // 按 Enter：接著標下一個還沒標讀音的字（台語 / 粵語逐字輸入用）。
  if (advance) openNextSlot(lineIndex, seg.start);
}

function openNextSlot(lineIndex, start) {
  const next = [...document.querySelectorAll("#lines ruby.slot")].find((node) => {
    const li = Number(node.dataset.line);
    return li > lineIndex || (li === lineIndex && Number(node.dataset.start) > start);
  });
  if (!next) return;
  const seg = editor.doc.lines[Number(next.dataset.line)].segments.find((s) => s.start === Number(next.dataset.start));
  if (seg) {
    next.scrollIntoView({ block: "nearest" });
    openRuby(next, Number(next.dataset.line), seg);
  }
}

$("#ruby-apply").addEventListener("click", () => applyRuby($("#ruby-input").value.trim()));
$("#ruby-reset").addEventListener("click", () => applyRuby(""));
$("#ruby-input").addEventListener("keydown", (e) => {
  if (e.key === "Enter") { e.preventDefault(); applyRuby($("#ruby-input").value.trim(), { advance: true }); }
});
document.addEventListener("mousedown", (e) => {
  if (editor.ruby && !$("#ruby-pop").contains(e.target) && !editor.ruby.target.contains(e.target)) closeRuby();
});

function markDirty() {
  editor.dirty = true;
  setStatus("尚未儲存");
}

function setStatus(text) {
  $("#editor-status").textContent = text;
  $("#editor-status").title = text;
}

async function saveLyrics() {
  await syncEditor();
  const data = await api(`/api/items/${enc(editor.item.name)}/lyrics`, { method: "PUT", body: { text: editor.views.text } });
  Object.assign(editor, { doc: data.doc, dirty: false });
  editor.views = { text: data.text, annotated: data.annotated, plain: data.plain };
  editor.paren = data.paren || [];
  renderParenBar();
  if (TEXT_MODES.has(editor.mode)) $("#editor-text").value = editor.views[editor.mode];
  else renderLines();
  setStatus(`已儲存：${data.path}`);
  refresh();
  return data;
}

$("#save").addEventListener("click", async () => {
  try {
    await saveLyrics();
    closeEditor(true);   // 儲存成功就關閉；失敗時留著視窗，內容不會遺失
    toast("歌詞已儲存");
  } catch (e) {
    toast(e.message, true);
  }
});

$("#save-run").addEventListener("click", async () => {
  try {
    const data = await saveLyrics();
    if (!data.doc.lines.some((l) => l.kind === "lyric")) {
      toast("歌詞是空的，無法製作伴唱帶", true);
      return;
    }
    const item = editor.item;
    closeEditor(true);
    await runSteps(item, ["karaoke"]);
  } catch (e) {
    toast(e.message, true);
  }
});

for (const btn of document.querySelectorAll("#mode-toggle button")) {
  btn.addEventListener("click", () => setMode(btn.dataset.mode));
}
$("#editor-text").addEventListener("input", markDirty);

function closeEditor(force = false) {
  if (!force && editor.dirty && !confirm("歌詞還沒儲存，確定要關閉嗎？")) return;
  closeRuby();
  closeTimePop();
  editor.dirty = false;
  $("#editor").hidden = true;
}

// ---- 播放（邊播邊預覽 / 編輯歌詞） ---------------------------------------------
//
// 所有播放都在這個畫面：左邊播放影片，右邊是歌詞清單，可以邊聽邊：
//   · 修改歌詞文字、切換演唱者（存回歌詞檔；改了文字的話更新伴唱帶時會重新對時）
//   · 調整時間：選一句按 Enter 設成「從現在開始」，或 [ ] 微調（只移這一句）；整段偏掉交給 AI 重對
//   · 檢查對時標出的疑慮句：點 ⚠ 或「上一個 / 下一個」直接跳到那句播放
// 播放沒燒字幕的版本（原始影片、伴奏、人聲）時，網頁會即時畫出逐字填色的字幕，調整後立刻看得到；
// 已燒字幕的成品（伴唱帶）不另外疊字幕，一調整就自動切到伴奏預覽。

const BURNED = new Set(["karaoke", "karaoke_original"]);   // 已經燒上字幕的版本
const SUB_PREVIEW = 4;      // 還沒開始唱時，提前幾秒先顯示第一句（或間奏後的下一句）
const SUB_LONG_GAP = 8;     // 兩句之間空這麼久算間奏：唱完 SUB_LINGER 秒後先收起來
const SUB_LINGER = 2;

const studio = {
  item: null, doc: null, timing: [], qa: new Map(),
  media: null,        // 正在播放的版本
  lyricRows: [],      // 依序每一句歌詞：{ docIndex, row }
  selected: 0,        // 選取的句子（從 0 起算，與對時結果一致）
  playing: -1,        // 正在唱的句子
  subLine: null,      // 字幕目前畫的是哪一句（避免每一格都重建 DOM）
  textChanged: false, retimed: false, raf: 0,
};

const studioVideo = () => $("#studio-video");

/**
 * 開始播放。key 指定版本（預設第一個，也就是最終輸出的伴唱帶）；
 * line 指定從第幾句開始（會選取那句、從它前 1.5 秒播放）；jumpToQa 從第一個疑慮句開始。
 */
async function openStudio(item, { key = null, at = null, line = null, jumpToQa = false } = {}) {
  const media = item.media.filter((m) => m.url);
  if (!media.length) {
    toast("還沒有可以播放的檔案", true);
    return;
  }
  let lyricsData, timingData, qaData;
  try {
    [lyricsData, timingData, qaData] = await Promise.all([
      api(`/api/items/${enc(item.name)}/lyrics`),
      api(`/api/items/${enc(item.name)}/timing`).catch(() => ({ lines: [] })),
      api(`/api/items/${enc(item.name)}/qa`).catch(() => ({ lines: [] })),
    ]);
  } catch (e) {
    toast(e.message, true);
    return;
  }
  Object.assign(studio, {
    item, doc: lyricsData.doc, timing: timingData.lines, qa: new Map(qaData.lines.map((c) => [c.index, c])),
    selected: 0, playing: -1, subLine: null, textChanged: false, retimed: false, pending: null,
  });
  $("#studio-song").textContent = item.artist ? `${item.artist} - ${item.title}` : item.title;
  $("#studio-tabs").replaceChildren(...media.map((m) => el("button", {
    type: "button", dataset: { key: m.key }, title: m.desc || "",
    onclick: () => setStudioSource(m, studioVideo().currentTime),
  }, m.label)));
  $("#studio-status").textContent = "";
  $("#studio-run").textContent = item.stages.karaoke === "pending" || item.stages.karaoke === "no_lyrics" ? "製作伴唱帶" : "更新伴唱帶";
  renderApproveButton();
  $("#studio-run").disabled = item.stages.lyrics !== "done";
  $("#studio").hidden = false;
  renderStudioLines();
  updateStudioWarn();

  if (jumpToQa) line = studioQaIndexes()[0] ?? line;
  let start = at ?? 0;
  if (line != null && studio.timing[line]) {
    start = Math.max(0, studio.timing[line].start - 1.5);
    selectStudioLine(line, { seek: false });
  } else {
    const near = studio.timing.findIndex((t) => t.end >= start);
    selectStudioLine(near >= 0 ? near : 0, { seek: false });
  }
  setStudioSource(media.find((m) => m.key === key) || media[0], start, true);
  cancelAnimationFrame(studio.raf);
  studio.raf = requestAnimationFrame(studioTick);
}

/** 播放畫面的「確認沒問題」：伴唱帶完成且沒有尚未套用的調整才能確認。 */
function renderApproveButton() {
  const item = studio.item;
  const btn = $("#studio-approve");
  const approved = item.approval === "approved";
  const pending = studio.retimed || studio.textChanged;
  btn.classList.toggle("approved", approved && !pending);
  // 有尚未套用的調整時不能確認（要先重新製作），也不能取消（避免按鈕文字和動作對不上）
  btn.disabled = pending || (!approved && item.stages.karaoke !== "done");
  $("span", btn).textContent = approved && !pending ? "已確認（取消）" : "確認沒問題";
  btn.title = approved && !pending ? "已確認這一版沒問題；點一下取消確認"
    : pending ? "有尚未套用的調整：請先按「更新伴唱帶」，做好後再確認"
      : item.stages.karaoke !== "done" ? "伴唱帶還沒做好或需要更新，做好後才能確認"
        : "確認目前這一版伴唱帶沒問題；之後成品有變會自動變回需重新確認";
}

$("#studio-approve").addEventListener("click", async () => {
  const item = studio.item;
  const approve = item.approval !== "approved";
  try {
    await setApproval(item, approve);
    item.approval = approve ? "approved" : null;
    renderApproveButton();
    toast(approve ? "已確認這一版伴唱帶沒問題" : "已取消確認");
    refresh();
  } catch (e) {
    toast(e.message, true);
  }
});

/** 即時字幕的字級：影片實際顯示的高度 × 字幕比例（和燒進影片的一樣大）。 */
function fitStudioSub() {
  const video = studioVideo();
  const frame = video.parentElement;
  if (!frame) return;
  const ratio = state.settings?.subtitle_ratio ?? BASE_RATIO;
  const shown = video.videoWidth && video.videoHeight
    ? Math.min(frame.clientHeight, frame.clientWidth * video.videoHeight / video.videoWidth)
    : frame.clientHeight;
  $("#studio-sub").style.fontSize = `${Math.max(10, shown * ratio)}px`;
}

new ResizeObserver(fitStudioSub).observe($(".stage-frame"));
studioVideo().addEventListener("loadedmetadata", fitStudioSub);

function closeStudio() {
  const video = studioVideo();
  video.pause();
  video.removeAttribute("src");
  video.load();
  cancelAnimationFrame(studio.raf);
  studio.pending = null;   // 工作照樣會完成並存檔，只是不再更新這個畫面
  $("#studio").hidden = true;
  if (studio.retimed || studio.textChanged) toast("已儲存調整。伴唱帶顯示「需更新」，按「更新伴唱帶」重新製作後生效");
  refresh();
}

function setStudioSource(media, at, autoplay = null) {
  const video = studioVideo();
  const play = autoplay ?? !video.paused;
  studio.media = media;
  for (const btn of $("#studio-tabs").children) btn.classList.toggle("on", btn.dataset.key === media.key);
  const burned = BURNED.has(media.key);
  $("#studio-sub").hidden = burned;
  $("#studio-note").hidden = !burned || !studio.timing.length;
  video.src = media.url;
  video.playbackRate = parseFloat($("#studio-rate").value);
  video.addEventListener("loadedmetadata", () => {
    video.currentTime = at || 0;
    $("#studio-seek").max = String(video.duration || 1);
    if (play) video.play().catch(() => {});
  }, { once: true });
}

/** 正在看已燒字幕的成品時做了調整：換到沒有字幕的版本，才看得到即時預覽。 */
function ensureLivePreview() {
  if (!studio.media || !BURNED.has(studio.media.key)) return;
  const live = ["instrumental", "source"].map((key) => studio.item.media.find((m) => m.key === key && m.url)).find(Boolean);
  if (!live) return;
  setStudioSource(live, studioVideo().currentTime);
  toast(`已切換到「${live.label}」，即時預覽調整後的字幕`);
}

/** 歌詞檔裡第幾句（依序）對應的對時結果；文字對不上時 mismatch 為真（歌詞改過、還沒重新對時）。 */
function studioTiming(index, text) {
  const t = studio.timing[index];
  if (!t) return null;
  return { ...t, mismatch: t.text.replace(/\s/g, "") !== text.replace(/\s/g, "") };
}

/** 第 index 句的對時檢查結果；文字改過就不再套用。 */
function studioQa(index, text) {
  const check = studio.qa.get(index);
  return check && check.text.replace(/\s/g, "") === text.replace(/\s/g, "") ? check : null;
}

function studioQaIndexes() {
  return studio.lyricRows.map((r, i) => (studioQa(i, studio.doc.lines[r.docIndex].text) ? i : -1)).filter((i) => i >= 0);
}

function renderStudioLines() {
  const box = $("#studio-lines");
  box.replaceChildren();
  studio.lyricRows = [];
  if (!studio.doc.lines.some((l) => l.kind === "lyric")) {
    box.append(el("div", { class: "empty-lyrics" }, "還沒有歌詞。",
      el("button", {
        type: "button", class: "view-link attention", onclick: () => { const item = studio.item; closeStudio(); openEditor(item); },
      }, icon("lyrics"), "輸入歌詞")));
    renderStudioQa();
    return;
  }
  let no = 0;
  studio.doc.lines.forEach((line, docIndex) => {
    if (line.kind === "blank") return box.append(el("div", { class: "blank-line" }));
    if (line.kind === "comment") return box.append(el("div", { class: "comment-line" }, line.text));
    const index = no++;
    const time = studioTiming(index, line.text);
    const check = studioQa(index, line.text);
    const nudge = (d) => el("button", {
      type: "button", disabled: !time, title: `這句的開始 ${d > 0 ? "延後" : "提前"} ${Math.abs(d)} 秒`,
      onclick: (e) => { e.stopPropagation(); shiftStudioLine(index, d); },
    }, d > 0 ? `+${d}` : `−${-d}`);
    const aiDisabled = !time || time.mismatch || Boolean(studio.pending);
    const row = el("div", {
      class: "studio-line" + (time?.mismatch ? " mismatch" : "") + (check ? ` qa-${check.status}` : ""),
      onclick: () => selectStudioLine(index),
    },
    el("span", { class: "line-no" }, index + 1),
    el("button", {
      type: "button", class: "singer", dataset: line.singer ? { singer: line.singer } : {},
      title: "演唱者：點一下切換", onclick: (e) => { e.stopPropagation(); cycleStudioSinger(docIndex); },
    }, line.singer || "—"),
    el("div", { class: "studio-text", title: "點兩下修改歌詞", ondblclick: (e) => { e.stopPropagation(); editStudioText(index); } },
      el("span", { class: "studio-words" }, line.text,
        line.translation ? el("small", { class: "studio-trans" }, line.translation) : null),
      el("button", {
        type: "button", class: "edit-btn", title: "修改歌詞", "aria-label": "修改歌詞",
        onclick: (e) => { e.stopPropagation(); editStudioText(index); },
      }, icon("edit"))),
    check ? el("button", {
      type: "button", class: "qa-flag",
      title: `${check.status === "wrong" ? "可能不準" : "待確認"}：${check.reasons.join("；")}\n點一下從這句開始播放`,
      "aria-label": "播放這句", onclick: (e) => { e.stopPropagation(); playStudioLine(index); },
    }, icon("warn")) : el("span"),
    el("span", { class: "studio-time", title: time?.mismatch ? "歌詞改過，這是改之前的時間" : "開始時間" },
      time ? fmtClock(time.start) : "—"),
    // 工具列：一排放得下的精簡按鈕（選取的句子與前後各一句會展開）。
    el("div", { class: "studio-tools" },
      el("div", { class: "nudge-group", role: "group", "aria-label": "微調開始時間" },
        nudge(-0.5), nudge(-0.1), nudge(0.1), nudge(0.5)),
      el("button", {
        type: "button", class: "btn small primary", disabled: !time,
        title: "把這句的開始設成目前播放的位置（選取的句子也可以按 Enter）",
        onclick: (e) => { e.stopPropagation(); setStudioLineNow(index); },
      }, "設為現在"),
      el("button", {
        type: "button", class: "icon-btn tool-icon", disabled: !time, title: "從這句開始前 1.5 秒播放",
        "aria-label": "播放這句", onclick: (e) => { e.stopPropagation(); playStudioLine(index); },
      }, icon("play")),
      el("button", {
        type: "button", class: "btn small ai-btn", disabled: aiDisabled, "aria-haspopup": "menu",
        title: "以這句目前的開頭為準，讓 AI 重新對時", onclick: (e) => { e.stopPropagation(); openRetimeMenu(e.currentTarget, index); },
      }, "AI 重對", icon("caret"))));
    studio.lyricRows.push({ docIndex, row });
    box.append(row);
  });
  markStudioRows();
  renderStudioQa();
}

/** 疑慮句的導覽列：數量 + 上一個 / 下一個（跳過去並播放）。 */
function renderStudioQa() {
  const indexes = studioQaIndexes();
  const box = $("#studio-qa");
  box.hidden = !indexes.length;
  if (!indexes.length) return;
  const wrong = indexes.filter((i) => studio.qa.get(i).status === "wrong").length;
  const suspect = indexes.length - wrong;
  const go = (dir) => {
    const cur = studio.selected;
    const target = dir > 0 ? indexes.find((i) => i > cur) ?? indexes[0] : [...indexes].reverse().find((i) => i < cur) ?? indexes.at(-1);
    playStudioLine(target);
  };
  box.replaceChildren(icon("warn"),
    el("span", { class: "grow" }, `對時檢查：${[wrong ? `${wrong} 句可能不準` : null, suspect ? `${suspect} 句待確認` : null].filter(Boolean).join("、")}`),
    el("button", { type: "button", class: "btn small", onclick: () => go(-1) }, "上一個"),
    el("button", { type: "button", class: "btn small", onclick: () => go(1) }, "下一個"));
}

/** 只有正在播放的句子高亮；選取的句子與前後各一句展開工具列。 */
function markStudioRows() {
  studio.lyricRows.forEach(({ row }, i) => {
    row.classList.toggle("selected", i === studio.selected);
    row.classList.toggle("tools-open", Math.abs(i - studio.selected) <= 1);
    row.classList.toggle("playing", i === studio.playing);
  });
}

function openRetimeMenu(anchor, index) {
  const count = studio.timing.length - index;
  showMenu(anchor, `以第 ${index + 1} 句目前的開頭為準（先播到開唱的瞬間設定好）`, [
    menuEntry(`重對這句及之後全部（${count} 句）`,
      `AI 重新對第 ${index + 1} 句到最後一句；這句之前的不動。約 10–30 秒`, () => retimeStudio(index, "from")),
    menuEntry("只重對這句", "範圍到下一句的開頭，重新抓這句每個字的時間。幾秒完成", () => retimeStudio(index, "line")),
  ]);
}

function selectStudioLine(index, { seek = true } = {}) {
  const count = studio.lyricRows.length;
  if (!count) return;
  studio.selected = Math.max(0, Math.min(count - 1, index));
  markStudioRows();
  studio.lyricRows[studio.selected].row.scrollIntoView({ block: "nearest" });
  const t = studio.timing[studio.selected];
  if (seek && t) studioVideo().currentTime = Math.max(0, t.start - 1.5);
}

function playStudioLine(index) {
  selectStudioLine(index);
  studioVideo().play().catch(() => {});
}

async function shiftStudioLine(index, delta) {
  delta = Math.round(delta * 1000) / 1000;
  if (!studio.timing[index] || Math.abs(delta) < 0.01) return false;
  const following = false;   // 只移這一句；整段偏掉交給 AI 重對
  try {
    const data = await api(`/api/items/${enc(studio.item.name)}/timing`, {
      method: "POST", body: { line: index, delta, following },
    });
    studio.timing = data.lines;
    studio.retimed = true;
    renderApproveButton();
    studio.subLine = null;   // 重畫字幕
    for (const [i, { row }] of studio.lyricRows.entries()) {
      const t = studio.timing[i];
      if (t) $(".studio-time", row).textContent = fmtClock(t.start);
    }
    const pushed = data.pushed || [];
    $("#studio-status").textContent = `第 ${index + 1} 句 ${delta > 0 ? "+" : ""}${delta} 秒`
      + (pushed.length ? `，第 ${pushed.map((k) => k + 1).join("、")} 句跟著往後挪` : "") + "，已儲存";
    ensureLivePreview();
    return true;
  } catch (e) {
    toast(e.message, true);
    return false;
  }
}

/** 以第 index 句目前的開頭為準讓 AI 重新對時（排進 AI 處理佇列，完成後自動更新畫面）。 */
async function retimeStudio(index, mode) {
  if (studio.pending) return;
  try {
    const job = await api("/api/jobs", {
      method: "POST", body: { steps: ["retime"], item: studio.item.name, line: index, mode },
    });
    studio.pending = { id: job.id, index, mode, item: studio.item.name, count: studio.timing.length - index };
    $("#studio-status").textContent = `${retimeLabel(studio.pending)}：排隊中…`;
    renderStudioLines();   // 進行中先停用 AI 按鈕
    refresh();
  } catch (e) {
    toast(e.message, true);
  }
}

const retimeLabel = (p) => (p.mode === "from"
  ? `AI 重新對時第 ${p.index + 1} 句及之後全部（共 ${p.count} 句）`
  : `AI 重新對時第 ${p.index + 1} 句（只有這句）`);

/** 每次更新狀態時檢查 AI 重新對時的工作；完成就載入新的時間並從那句開始播放。 */
async function watchStudioJob(jobs) {
  const p = studio.pending;
  if (!p || $("#studio").hidden || studio.item?.name !== p.item) return;
  const job = jobs.find((j) => j.id === p.id);
  if (!job) {
    studio.pending = null;
    return;
  }
  if (job.status === "queued" || job.status === "running") {
    $("#studio-status").textContent = `${retimeLabel(p)}：${job.status === "queued" ? "排隊中" : "處理中"}…`;
    return;
  }
  studio.pending = null;
  if (job.status !== "done") {
    $("#studio-status").textContent = "";
    toast(job.status === "failed" ? `${retimeLabel(p)}失敗：${job.error || ""}` : `已取消${retimeLabel(p)}`,
      job.status === "failed");
    renderStudioLines();
    return;
  }
  try {
    const data = await api(`/api/items/${enc(p.item)}/timing`);
    studio.timing = data.lines;
    studio.retimed = true;
    renderApproveButton();
    studio.subLine = null;
    renderStudioLines();
    updateStudioWarn();
    $("#studio-status").textContent = `${retimeLabel(p)}完成，播放確認看看`;
    ensureLivePreview();
    playStudioLine(p.index);
  } catch (e) {
    toast(e.message, true);
  }
}

/** 選取的句子從目前播放的位置開始，然後選到下一句（可以一路聽一路按 Enter）。 */
async function setStudioLineNow(index = studio.selected) {
  const t = studio.timing[index];
  if (!t) return;
  const ok = await shiftStudioLine(index, studioVideo().currentTime - t.start);
  if (ok && index + 1 < studio.lyricRows.length) selectStudioLine(index + 1, { seek: false });
}

async function saveStudioLyrics() {
  const converted = await api("/api/lyrics/convert", {
    method: "POST", body: { doc: studio.doc, language: studio.item.language || null },
  });
  const data = await api(`/api/items/${enc(studio.item.name)}/lyrics`, { method: "PUT", body: { text: converted.text } });
  studio.doc = data.doc;
}

async function cycleStudioSinger(docIndex) {
  const line = studio.doc.lines[docIndex];
  line.singer = SINGER_ORDER[(SINGER_ORDER.indexOf(line.singer ?? null) + 1) % SINGER_ORDER.length];
  try {
    await saveStudioLyrics();   // 演唱者只影響顏色，不會重新對時
    studio.subLine = null;
    studio.retimed = true;      // 伴唱帶的顏色要重新製作才會更新
    renderApproveButton();
    renderStudioLines();
    ensureLivePreview();
  } catch (e) {
    toast(e.message, true);
  }
}

function editStudioText(index) {
  const { docIndex, row } = studio.lyricRows[index];
  const line = studio.doc.lines[docIndex];
  const box = $(".studio-text", row);
  const input = el("input", { class: "inline-edit", value: line.text, spellcheck: "false", "aria-label": "歌詞" });
  box.replaceChildren(input);
  let done = false;
  const finish = async (commit) => {
    if (done) return;
    done = true;
    const text = input.value.trim();
    if (commit && text && text !== line.text) {
      line.text = text;
      line.rubies = [];   // 文字改了，原本手動指定的讀音位置對不上
      try {
        await saveStudioLyrics();
        studio.textChanged = true;
        renderApproveButton();
        $("#studio-status").textContent = `第 ${index + 1} 句歌詞已儲存`;
      } catch (e) {
        toast(e.message, true);
      }
    }
    renderStudioLines();
    updateStudioWarn();
  };
  input.addEventListener("keydown", (e) => {
    e.stopPropagation();   // 輸入時不觸發播放快捷鍵
    if (e.key === "Enter") { e.preventDefault(); finish(true); }
    if (e.key === "Escape") { e.preventDefault(); finish(false); }
  });
  input.addEventListener("blur", () => finish(true));
  input.addEventListener("click", (e) => e.stopPropagation());
  input.focus();
  input.select();
}

function updateStudioWarn() {
  const box = $("#studio-warn");
  const hasLines = studio.lyricRows.length > 0;
  if (hasLines && !studio.timing.length) {
    box.hidden = false;
    box.textContent = "還沒有對時結果：製作伴唱帶之後才有每句的時間與字幕預覽，現在可以先邊聽邊修改歌詞。";
    return;
  }
  const mismatch = studio.lyricRows.length !== studio.timing.length
    || studio.lyricRows.some((r, i) => studioTiming(i, studio.doc.lines[r.docIndex].text)?.mismatch);
  box.hidden = !hasLines || (!mismatch && !studio.textChanged);
  box.textContent = "歌詞文字和目前的對時結果不同：按「更新伴唱帶」會重新對時（約一分鐘），這裡手動調整的時間會被取代。"
    + "建議先改完文字並更新伴唱帶，再回來調時間。";
}

// 即時字幕：上排目前這句、下排下一句，逐字填色（和燒進影片的樣式相近，只是預覽）。
function studioTick() {
  studio.raf = requestAnimationFrame(studioTick);
  const video = studioVideo();
  const now = video.currentTime;
  $("#studio-clock").textContent = fmtClock(now);
  if (document.activeElement !== $("#studio-seek")) $("#studio-seek").value = String(now);
  const playIcon = $("#studio-play use");
  const want = video.paused ? "#i-play" : "#i-pause";
  if (playIcon.getAttribute("href") !== want) {
    playIcon.setAttribute("href", want);
    $("#studio-play span").textContent = video.paused ? "播放" : "暫停";
  }
  if (!studio.timing.length) return;

  // 目前這句：已經開始唱的最後一句。一直顯示到下一句開始才換掉（拖長音唱到下一句前都還看得到）；
  // 只有遇到長間奏時，唱完一會兒先收起來，等下一句快開始再出現。
  let current = -1;
  studio.timing.forEach((t, i) => { if (t.start <= now) current = i; });
  const following = studio.timing[current + 1];
  const longGap = current >= 0 && following && following.start - studio.timing[current].end > SUB_LONG_GAP;
  const show = current >= 0 && !(longGap && now > studio.timing[current].end + SUB_LINGER)
    && !(!following && now > studio.timing[current].end + SUB_LINGER) ? current : -1;
  const playing = current >= 0 && now <= studio.timing[current].end + 0.3 ? current : -1;
  if (playing !== studio.playing) {
    studio.playing = playing;
    if (playing >= 0 && !video.paused && (playing > studio.selected || playing < studio.selected - 1)) {
      studio.selected = playing;
    }
    markStudioRows();
    if (playing >= 0 && !video.paused) studio.lyricRows[playing]?.row.scrollIntoView({ block: "nearest", behavior: "smooth" });
  }
  // 上排：正在唱的句子；前奏、間奏時預先顯示幾秒內要唱的句子。下排：再下一句。
  let top = show;
  if (top < 0) {
    const next = studio.timing.findIndex((t) => t.start > now);
    if (next >= 0 && studio.timing[next].start - now <= SUB_PREVIEW) top = next;
  }
  const bottom = top >= 0 && top + 1 < studio.timing.length ? top + 1 : -1;
  // 翻譯：只顯示正在唱的那一句（和燒進影片的一樣），歌曲設定不燒翻譯時不顯示
  const transRow = show >= 0 && studio.item.translation !== false ? studio.lyricRows[show] : null;
  const trans = transRow ? studio.doc.lines[transRow.docIndex].translation || "" : "";
  const key = `${top}|${bottom}|${trans}`;
  if (key !== studio.subLine) {
    studio.subLine = key;
    $("#studio-sub").replaceChildren(
      el("div", { class: "sub-row trans" }, trans),
      el("div", { class: "sub-row current" }, top >= 0 ? subWords(top) : null),
      el("div", { class: "sub-row next" }, bottom >= 0 ? subWords(bottom) : null));
  }
  for (const span of $("#studio-sub").querySelectorAll(".w")) {
    const start = parseFloat(span.dataset.s);
    const end = parseFloat(span.dataset.e);
    const p = now <= start ? 0 : now >= end ? 1 : (now - start) / (end - start);
    span.style.setProperty("--p", `${(p * 100).toFixed(1)}%`);
  }
}

function subWords(index) {
  const t = studio.timing[index];
  const row = studio.lyricRows[index];
  const line = row ? studio.doc.lines[row.docIndex] : null;
  const attrs = { class: "sub-line", dataset: line?.singer ? { singer: line.singer } : {} };
  const words = t.words.length ? t.words : [{ text: t.text, start: t.start, end: t.end }];
  const ruby = studio.doc.language === "ja" && line?.segments?.some((s) => s.ruby);
  const chars = ruby && !studioTiming(index, line.text).mismatch ? charTimes(line.text, words) : null;
  if (!chars) {
    return el("span", attrs, words.map((w) => el("span", { class: "w", dataset: { s: w.start, e: w.end } }, w.text)));
  }
  // 日文：依歌詞的假名片段畫 <ruby>，漢字逐字填色，上方的假名跟著整段漢字一起填。
  const text = Array.from(line.text);
  const charSpan = (i) => el("span", { class: "w", dataset: { s: chars[i].s, e: chars[i].e } }, text[i]);
  return el("span", attrs, line.segments.map((seg) => {
    const base = [];
    for (let i = seg.start; i < seg.end; i++) base.push(charSpan(i));
    if (!seg.ruby) return base;
    return el("ruby", {}, base, el("rt", {},
      el("span", { class: "w", dataset: { s: chars[seg.start].s, e: chars[seg.end - 1].e } }, seg.ruby)));
  }).flat());
}

/**
 * 把逐字時間攤到歌詞的每個字元上（同一段裡的字平均分配時間），回傳和歌詞等長的 [{ s, e }]。
 * 對時的文字和歌詞只差空白時照樣對得上；其他對不上的情況回傳 null（改用不加假名的顯示）。
 */
function charTimes(lineText, words) {
  const timed = [];
  for (const w of words) {
    const chars = Array.from(w.text).filter((c) => c.trim());
    chars.forEach((c, k) => timed.push({
      c, s: w.start + (w.end - w.start) * (k / chars.length), e: w.start + (w.end - w.start) * ((k + 1) / chars.length),
    }));
  }
  const out = [];
  let j = 0;
  let last = words[0]?.start ?? 0;
  for (const c of Array.from(lineText)) {
    if (!c.trim()) {
      out.push({ s: last, e: last });   // 空白：跟著前一個字
      continue;
    }
    if (j >= timed.length || timed[j].c !== c) return null;
    out.push(timed[j]);
    last = timed[j].e;
    j += 1;
  }
  return j === timed.length ? out : null;
}

$("#studio-play").addEventListener("click", () => {
  const video = studioVideo();
  if (video.paused) video.play().catch(() => {}); else video.pause();
});
$("#studio-video").addEventListener("click", () => $("#studio-play").click());
$("#studio-seek").addEventListener("input", (e) => { studioVideo().currentTime = parseFloat(e.target.value); });
$("#studio-rate").addEventListener("change", (e) => { studioVideo().playbackRate = parseFloat(e.target.value); });
$("#studio-run").addEventListener("click", async () => {
  const item = studio.item;
  studio.retimed = studio.textChanged = false;   // 馬上就要重新製作，不必再提醒
  closeStudio();
  await runSteps(item, ["karaoke"]);
});

document.addEventListener("keydown", (e) => {
  if ($("#studio").hidden || e.ctrlKey || e.metaKey || e.altKey) return;
  const target = e.target instanceof Element ? e.target : null;
  if (target?.closest("input:not([type=range]):not([type=checkbox]), select, textarea")) return;
  const video = studioVideo();
  const actions = {
    " ": () => $("#studio-play").click(),
    ArrowLeft: () => { video.currentTime = Math.max(0, video.currentTime - 2); },
    ArrowRight: () => { video.currentTime = video.currentTime + 2; },
    ArrowUp: () => selectStudioLine(studio.selected - 1),
    ArrowDown: () => selectStudioLine(studio.selected + 1),
    Enter: () => setStudioLineNow(),
    "[": () => shiftStudioLine(studio.selected, -0.1),
    "]": () => shiftStudioLine(studio.selected, 0.1),
    "{": () => shiftStudioLine(studio.selected, -0.5),
    "}": () => shiftStudioLine(studio.selected, 0.5),
  };
  const action = actions[e.key];
  if (!action) return;
  e.preventDefault();
  action();
});

/** 從歌詞編輯器切到播放畫面（有未儲存的修改先問要不要儲存）。 */
async function openStudioFromEditor(options = {}) {
  const item = editor.item;
  if (editor.dirty) {
    if (!confirm("歌詞有尚未儲存的修改，要先儲存再播放嗎？")) return;
    try {
      await saveLyrics();
    } catch (e) {
      toast(e.message, true);
      return;
    }
  }
  closeEditor(true);
  openStudio(item, options);
}

$("#editor-studio").addEventListener("click", () => openStudioFromEditor());
$("#studio-lyrics").addEventListener("click", () => {
  const item = studio.item;
  closeStudio();
  openEditor(item);
});

// ---- 對話框共用 -------------------------------------------------------------

function closeDialog(sel) {
  $(sel).hidden = true;
}

const CLOSERS = {
  "#editor": () => closeEditor(),
  "#studio": closeStudio,
  "#song-dialog": () => closeDialog("#song-dialog"),
  "#folder-dialog": () => closeDialog("#folder-dialog"),
  "#settings-dialog": () => closeDialog("#settings-dialog"),
};

for (const [sel, close] of Object.entries(CLOSERS)) {
  const overlay = $(sel);
  overlay.addEventListener("mousedown", (e) => { if (e.target === overlay) close(); });
  for (const btn of overlay.querySelectorAll("[data-close]")) btn.addEventListener("click", () => close());
}

document.addEventListener("keydown", (e) => {
  if (e.key !== "Escape") return;
  if (editor.ruby) return closeRuby();
  if (editor.time) return closeTimePop();
  if (!$("#menu").hidden) return closeMenu();
  // 由最上層的對話框開始關。
  for (const sel of ["#song-dialog", "#folder-dialog", "#settings-dialog", "#studio", "#editor"]) {
    if (!$(sel).hidden) return CLOSERS[sel]();
  }
});

window.addEventListener("beforeunload", (e) => {
  if (editor.dirty && !$("#editor").hidden) e.preventDefault();
});

loop();
