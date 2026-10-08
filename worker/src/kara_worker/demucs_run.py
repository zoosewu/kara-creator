"""用 Demucs 分離人聲（separate.run_demucs 以子程序執行）。

分離流程和 demucs.separate 完全相同（正規化 -> apply_model -> 還原音量 -> 每一軌各自防爆音縮放），
只有存檔改用 soundfile：torchaudio 2.9 起存檔需要另外安裝 torchcodec，而 Python 3.14 只有新版 torchaudio。

    python -m kara_worker.demucs_run TRACK.wav -o OUT [-n htdemucs] [--device cuda] [--two-stems vocals]
                                     [--shifts N] [-j N]

輸出到 OUT/<模型名稱>/<分軌>.wav（16-bit），--two-stems 時為 <分軌>.wav 與 no_<分軌>.wav。
進度條（tqdm）的格式和 demucs.separate 相同，separate.py 靠它回報進度。
"""
from __future__ import annotations

import argparse
import sys
from pathlib import Path

import soundfile as sf
import torch
from demucs.apply import BagOfModels, apply_model
from demucs.audio import convert_audio, prevent_clip
from demucs.pretrained import get_model


def main() -> int:
    parser = argparse.ArgumentParser(description="Demucs 人聲分離（soundfile 存檔）")
    parser.add_argument("track", type=Path)
    parser.add_argument("-o", "--out", type=Path, required=True)
    parser.add_argument("-n", "--name", default="htdemucs")
    parser.add_argument("--device", default="cuda" if torch.cuda.is_available() else "cpu")
    parser.add_argument("--two-stems", dest="stem")
    parser.add_argument("--shifts", type=int, default=1)
    parser.add_argument("--overlap", type=float, default=0.25)
    parser.add_argument("-j", "--jobs", type=int, default=0)
    args = parser.parse_args()

    model = get_model(args.name)
    if isinstance(model, BagOfModels):
        print(f"Selected model is a bag of {len(model.models)} models. "
              "You will see that many progress bars per track.", flush=True)
    model.cpu()
    model.eval()
    if args.stem is not None and args.stem not in model.sources:
        print(f"模型 {args.name} 沒有 {args.stem} 這一軌（有：{', '.join(model.sources)}）", file=sys.stderr)
        return 1
    out = args.out / args.name
    out.mkdir(parents=True, exist_ok=True)

    print(f"Separating track {args.track}", flush=True)
    data, rate = sf.read(str(args.track), dtype="float32", always_2d=True)
    wav = convert_audio(torch.from_numpy(data.T.copy()), rate, model.samplerate, model.audio_channels)
    ref = wav.mean(0)
    wav = (wav - ref.mean()) / ref.std()
    sources = apply_model(model, wav[None], device=args.device, shifts=args.shifts, split=True,
                          overlap=args.overlap, progress=True, num_workers=args.jobs)[0]
    sources = sources * ref.std() + ref.mean()

    stems = dict(zip(model.sources, sources, strict=False))
    if args.stem is not None:
        main_stem = stems.pop(args.stem)
        stems = {args.stem: main_stem, f"no_{args.stem}": sum(stems.values())}
    for name, source in stems.items():
        source = prevent_clip(source, mode="rescale")
        sf.write(str(out / f"{name}.wav"), source.cpu().numpy().T, model.samplerate, subtype="PCM_16")
    return 0


if __name__ == "__main__":
    sys.exit(main())
