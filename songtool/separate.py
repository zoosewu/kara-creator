"""用 Demucs 分離人聲與伴奏，結果放在 output/separated/<name>/。

來源預設是 output/downloads/ 的下載項目，輸出資料夾沿用下載資料夾的名稱。
輸出資料夾內的 separate.json 記錄來源檔的大小、修改時間與分離參數；
這些都沒變、輸出檔也都還在時，視為已處理並直接略過。

輸出格式與輸入相同：輸入帶影像軌（例如 mp4）時，影像軌原封不動複製，
只替換音軌，輸出仍是可播放的影片。
"""
from __future__ import annotations

import re
import subprocess
import sys
import tempfile
from dataclasses import dataclass, field
from pathlib import Path
from typing import Callable

from . import ai, config, manifest
from .media import VIDEO_EXTS, Cancelled, extract_wav, has_video_stream, mux

_PERCENT = re.compile(r"(\d+)%\|")

# Demucs 的分軌名稱 -> 輸出檔名後綴。
STEM_LABELS = {
    "vocals": "vocals",
    "no_vocals": "instrumental",
    "drums": "drums",
    "bass": "bass",
    "other": "other",
}


@dataclass
class SeparateOptions:
    stems: int = 2              # 2 = 人聲/伴奏，4 = 鼓/貝斯/人聲/其他
    model: str = "htdemucs"
    device: str = "auto"        # auto / cuda / cpu
    format: str | None = None   # None = 與輸入相同
    shifts: int = 0
    jobs: int = 0

    def output_ext(self, src: Path) -> str:
        if not self.format:
            return src.suffix.lower()
        ext = self.format.lower()
        return ext if ext.startswith(".") else "." + ext


@dataclass
class SeparateResult:
    source: Path
    status: str  # "separated" | "skipped" | "failed"
    out_dir: Path
    outputs: list[Path] = field(default_factory=list)
    error: str | None = None


def output_dir_for(src: Path) -> Path:
    """下載項目沿用下載資料夾名稱；其他位置的檔案用檔名。"""
    try:
        rel = src.resolve().relative_to(config.DOWNLOADS_DIR.resolve())
    except ValueError:
        rel = None
    name = rel.parts[0] if rel is not None and len(rel.parts) > 1 else src.stem
    return config.SEPARATED_DIR / name


def is_current(record: dict | None, src: Path, out_dir: Path) -> bool:
    """紀錄對應的來源檔沒變，且紀錄中的輸出檔都還在。"""
    if not record or not record.get("outputs") or not src.is_file():
        return False
    st = src.stat()
    if (record.get("source_size"), record.get("source_mtime_ns")) != (st.st_size, st.st_mtime_ns):
        return False
    return all((out_dir / name).is_file() for name in record["outputs"])


def matches_options(record: dict | None, src: Path, opts: SeparateOptions) -> bool:
    if not record:
        return False
    return (record.get("model"), record.get("stems"), record.get("format"),
            record.get("shifts")) == (opts.model, opts.stems, opts.output_ext(src), opts.shifts)


def separate_file(
    src: Path,
    opts: SeparateOptions | None = None,
    *,
    force: bool = False,
    log: Callable[[str], None] = print,
    progress: Callable[[float], None] | None = None,
    should_stop: Callable[[], bool] | None = None,
) -> SeparateResult:
    """should_stop() 為真時中斷處理並拋出 media.Cancelled。"""
    opts = opts or SeparateOptions()
    src = Path(src)
    out_dir = output_dir_for(src)
    record_path = out_dir / manifest.SEPARATE
    record = manifest.read(record_path)

    if not force and matches_options(record, src, opts) and is_current(record, src, out_dir):
        return SeparateResult(src, "skipped", out_dir,
                              [out_dir / name for name in record["outputs"]])

    # 先移除舊紀錄：處理到一半失敗時，下次才不會誤判為已完成。
    record_path.unlink(missing_ok=True)
    try:
        outputs = _separate(src, out_dir, opts, log, progress, should_stop)
    except Cancelled:
        # 取消只會發生在 Demucs 執行中（還沒開始寫輸出檔），舊的成品完好，把紀錄還原。
        if record:
            manifest.write(record_path, record)
        raise
    except Exception as exc:
        return SeparateResult(src, "failed", out_dir, error=str(exc))

    # 刪掉上次留下、這次沒有產生的檔案（例如從 4 軌改回 2 軌）。
    produced = {p.name for p in outputs}
    for name in (record or {}).get("outputs", []):
        if name not in produced:
            (out_dir / name).unlink(missing_ok=True)

    st = src.stat()
    manifest.write(record_path, {
        "source": str(src.resolve()),
        "source_size": st.st_size,
        "source_mtime_ns": st.st_mtime_ns,
        "model": opts.model,
        "stems": opts.stems,
        "format": opts.output_ext(src),
        "shifts": opts.shifts,
        "outputs": sorted(produced),
        "separated_at": manifest.now(),
    })
    return SeparateResult(src, "separated", out_dir, outputs)


def _separate(src: Path, out_dir: Path, opts: SeparateOptions,
              log: Callable[[str], None],
              progress: Callable[[float], None] | None = None,
              should_stop: Callable[[], bool] | None = None) -> list[Path]:
    keep_video = src.suffix.lower() in VIDEO_EXTS and has_video_stream(src)
    out_ext = opts.output_ext(src)
    log(f"  . 輸入 {src.suffix.lower()} -> 輸出 {out_ext}"
        f"{'（保留影像軌）' if keep_video else ''}")

    out_dir.mkdir(parents=True, exist_ok=True)
    outputs: list[Path] = []
    with tempfile.TemporaryDirectory(prefix="demucs_") as tmpdir:
        tmp = Path(tmpdir)
        # 暫存檔一律用 ASCII 檔名，避免中文路徑在子程序之間出問題。
        wav = tmp / "track.wav"
        log("  . 抽取音軌...")
        extract_wav(src, wav)

        # Demucs 交給 AI 伺服器（或 local 模式時在這個行程）執行，這裡只負責抽音軌與封裝。
        stem_dir = ai.separate(wav, tmp / "demucs", stems=opts.stems, model=opts.model, device=opts.device,
                               shifts=opts.shifts, jobs=opts.jobs, log=log, progress=progress,
                               should_stop=should_stop)

        for stem_file in sorted(stem_dir.glob("*.wav")):
            label = STEM_LABELS.get(stem_file.stem, stem_file.stem)
            dest = out_dir / f"{src.stem}_{label}{out_ext}"
            log(f"  . 封裝 {label} -> {dest.name}")
            mux(stem_file, src, dest, keep_video)
            outputs.append(dest)
    return outputs


def _resolve_device(requested: str) -> str:
    if requested != "auto":
        return requested
    try:
        import torch
        return "cuda" if torch.cuda.is_available() else "cpu"
    except Exception:
        return "cpu"


def _run_demucs(wav: Path, workdir: Path, opts: SeparateOptions,
                log: Callable[[str], None],
                progress: Callable[[float], None] | None = None,
                should_stop: Callable[[], bool] | None = None) -> Path:
    """跑 Demucs，回傳裝著各分軌 wav 的目錄。"""
    device = _resolve_device(opts.device)
    # 用專案自己的 demucs_run（流程同 demucs.separate，只是改用 soundfile 存檔，新版 torchaudio 不必另裝 torchcodec）。
    cmd = [
        sys.executable, "-m", "songtool.demucs_run",
        "-n", opts.model,
        "-o", str(workdir),
        "--device", device,
    ]
    if opts.stems == 2:
        cmd += ["--two-stems", "vocals"]
    if opts.shifts:
        cmd += ["--shifts", str(opts.shifts)]
    if opts.jobs:
        cmd += ["-j", str(opts.jobs)]
    cmd.append(str(wav))

    log(f"  . Demucs 分離中（model={opts.model}, device={device}, stems={opts.stems}）...")
    # Demucs 的輸出轉給 log（UI 才看得到）；進度條只每 10% 回報一次，避免洗版。
    # 文字模式會把 tqdm 用來覆寫同一行的 \r 也當成換行。
    proc = subprocess.Popen(cmd, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, cwd=config.PROJECT_ROOT,
                            text=True, encoding="utf-8", errors="replace")
    reported = -10
    try:
        for raw in proc.stdout:
            if should_stop and should_stop():
                raise Cancelled()
            line = raw.strip()
            if not line:
                continue
            m = _PERCENT.search(line)
            if not m:
                log(f"    {line}")
                continue
            pct = int(m.group(1))
            if progress:
                progress(pct / 100)
            if pct >= reported + 10:
                log(f"  . 分離進度 {pct}%")
                reported = pct
    finally:
        # 取消或出錯時不要留下還在跑的 Demucs（它會一直佔著顯示卡）。
        if proc.poll() is None:
            proc.kill()
            proc.wait()
    if proc.wait() != 0:
        raise RuntimeError(f"Demucs 執行失敗，returncode={proc.returncode}")

    # Demucs 會輸出到 <workdir>/<model>/ 之下。
    candidates = sorted(workdir.rglob("*.wav"))
    if not candidates:
        raise RuntimeError("Demucs 沒有產生任何輸出")
    return candidates[0].parent
