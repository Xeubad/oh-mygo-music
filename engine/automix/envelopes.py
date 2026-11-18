"""按模型估计的收句和前奏择点，导出真实汽水歌曲的本机试听原型。"""

import argparse
import json
import os
from pathlib import Path
import subprocess

os.environ["OMP_NUM_THREADS"] = "2"
os.environ["OPENBLAS_NUM_THREADS"] = "2"
os.environ["MPLBACKEND"] = "Agg"

import numpy as np
import soundfile as sf
from scipy.signal import butter, sosfiltfilt


ROOT = Path(__file__).resolve().parent
FFMPEG = ROOT / "bin" / "ffmpeg.exe"
RATE = 44100
STEM_NAMES = ("drums", "bass", "other", "vocals")


def segment(path, start, frames):
    with sf.SoundFile(path) as source:
        assert source.samplerate == RATE
        source.seek(max(0, min(round(start * RATE), len(source))))
        samples = source.read(frames, dtype="float32", always_2d=True)
    if len(samples) < frames:
        samples = np.pad(samples, ((0, frames - len(samples)), (0, 0)))
    return samples


def stems_for(track, start, frames):
    original = segment(track["input"], start, frames)
    stems = np.stack([
        segment(Path(track["stems"]) / f"{name}.wav", start, frames)
        for name in STEM_NAMES
    ])
    # 模型保存声部可能单独防削波；拟合回原混音增益，再把残差留在伴奏，保留中性还原。
    matrix = np.stack([stem[::47].reshape(-1) for stem in stems], axis=1)
    target = original[::47].reshape(-1)
    gains = np.clip(np.linalg.lstsq(matrix, target, rcond=None)[0], .25, 4)
    stems *= gains[:, None, None]
    stems[2] += original - stems.sum(axis=0)
    assert np.max(np.abs(stems.sum(axis=0) - original)) < 1e-5
    return stems, gains.tolist()


def stretch(stems, ratio, hold, recovery):
    if abs(ratio - 1) < 1e-5:
        return stems.copy()
    packed = stems.transpose(1, 0, 2).reshape(stems.shape[1], -1).astype("<f4")
    channels = packed.shape[1]
    # 命令位于变速器之前，时戳属于输入源时间；在混音结束后平滑回到原速。
    source_start = (hold + .5) * ratio
    commands = []
    for step in range(41):
        progress = step / 40
        smooth = progress * progress * (3 - 2 * progress)
        tempo = ratio + (1 - ratio) * smooth
        commands.append(f"{source_start + recovery * progress:.6f} rubberband tempo {tempo:.9f}")
    filters = (
        "asendcmd=c='" + ";".join(commands) + "',"
        f"rubberband=tempo={ratio:.10f}:pitch=1:transients=crisp:detector=compound:"
        "phase=laminar:formant=preserved:pitchq=quality:channels=together"
    )
    rendered = subprocess.run([
        str(FFMPEG), "-nostdin", "-hide_banner", "-loglevel", "error",
        "-f", "f32le", "-ar", str(RATE), "-ac", str(channels), "-i", "pipe:0",
        "-af", filters, "-ar", str(RATE), "-ac", str(channels),
        "-c:a", "pcm_f32le", "-f", "f32le", "pipe:1",
    ], input=packed.tobytes(), capture_output=True, check=True)
    output = np.frombuffer(rendered.stdout, dtype="<f4").copy().reshape(-1, len(STEM_NAMES), 2)
    return output.transpose(1, 0, 2)


def padded(samples, length, offset=0):
    output = np.zeros((length, 2), dtype=np.float32)
    if offset >= length:
        return output
    source_start = max(-offset, 0)
    destination = max(offset, 0)
    count = min(len(samples) - source_start, length - destination)
    if count > 0:
        output[destination:destination + count] = samples[source_start:source_start + count]
    return output


def split_bands(samples):
    low = sosfiltfilt(butter(2, 250, fs=RATE, output="sos"), samples, axis=0)
    upper = sosfiltfilt(butter(2, 2500, fs=RATE, output="sos"), samples, axis=0)
    return low, upper - low, samples - upper


def equal_power(old, new, times, start, end):
    progress = np.clip((times - start) / (end - start), 0, 1)
    return (
        old * np.cos(progress[:, None] * np.pi / 2)
        + new * np.sin(progress[:, None] * np.pi / 2)
    ).astype(np.float32)


def make_phrase_mix(old_stems, new_stems, original, candidate, lead, total):
    times = np.arange(total) / RATE
    overlap = candidate["overlap"]
    progress = np.clip((times - lead) / overlap, 0, 1)
    old_music = old_stems[:3].sum(axis=0)
    new_music = new_stems[:3].sum(axis=0)
    new_music = padded(new_music, total, round(lead * RATE))
    old_bands, new_bands = split_bands(old_music), split_bands(new_music)
    output = np.zeros((total, 2), dtype=np.float64)
    for band in range(3):
        phase = progress.copy()
        if band == 0:
            phase = np.clip((phase - .44) / .12, 0, 1)
            phase = phase * phase * (3 - 2 * phase)
        elif band == 2:
            phase += .6 * phase * (1 - phase)
        output += old_bands[band] * np.cos(phase[:, None] * np.pi / 2)
        output += new_bands[band] * np.sin(phase[:, None] * np.pi / 2)
    old_voice_end = lead + candidate["current_last_vocal_end"] - candidate["mix_start"]
    if candidate["current_last_vocal_end"] > candidate["mix_start"] + .2:
        voice_progress = np.clip((times - old_voice_end - .08) / .42, 0, 1)
        old_voice_gain = np.cos(voice_progress * np.pi / 2)
    else:
        old_voice_gain = np.cos(progress * np.pi / 2)
    new_voice_at = lead + (candidate["next_first_vocal"] - candidate["entry"]) / candidate["ratio"]
    new_voice_gain = np.sin(
        np.clip((times - new_voice_at + .6) / .45, 0, 1) * np.pi / 2
    )
    output += old_stems[3] * old_voice_gain[:, None]
    output += padded(new_stems[3], total, round(lead * RATE)) * new_voice_gain[:, None]
    output[times < lead] = original[times < lead]
    return output.astype(np.float32), {
        "old_voice_end_seconds": old_voice_end,
        "new_voice_start_seconds_estimated": new_voice_at,
        "old_voice_gain_at_last_vocal": float(np.interp(old_voice_end, times, old_voice_gain)),
        "new_voice_gain_at_first_vocal": float(np.interp(new_voice_at, times, new_voice_gain)),
    }


def write_float(path, samples):
    assert np.isfinite(samples).all()
    sf.write(path, samples, RATE, subtype="FLOAT")


def render_candidate(candidate, library, directory):
    current, next_track = library[candidate["current_id"]], library[candidate["next_id"]]
    directory.mkdir(parents=True, exist_ok=True)
    lead = 8.0
    clip_start = candidate["mix_start"] - lead
    first_next_voice = min(
        (region["start"] for region in next_track["vocal_regions_estimated"]),
        default=next_track["duration"],
    )
    ordinary_start = current["duration"] - 5 - clip_start
    ordinary_voice = ordinary_start + first_next_voice
    prototype_voice = lead + (candidate["next_first_vocal"] - candidate["entry"]) / candidate["ratio"]
    core_seconds = lead + candidate["overlap"] + 12
    seconds = min(78, max(core_seconds, ordinary_voice + 8))
    total = round(seconds * RATE)
    times = np.arange(total) / RATE
    old = segment(current["input"], clip_start, total)
    old_end = current["duration"] - clip_start
    old[times >= old_end] = 0
    ordinary_next = segment(next_track["input"], 0, max(0, total - round(ordinary_start * RATE)))
    ordinary_next = padded(ordinary_next, total, round(ordinary_start * RATE))
    ordinary = equal_power(old, ordinary_next, times, ordinary_start, old_end)
    old_stems, old_gains = stems_for(current, clip_start, total)
    source_frames = round((seconds + 5) * max(candidate["ratio"], 1) * RATE)
    next_stems, new_gains = stems_for(next_track, candidate["entry"], source_frames)
    processed = stretch(
        next_stems, candidate["ratio"], candidate["overlap"],
        recovery=4 * candidate["next_tempo"]["period"],
    )
    new_full = processed.sum(axis=0)
    new_placed = padded(new_full, total, round(lead * RATE))
    full_eq = equal_power(old, new_placed, times, lead, lead + candidate["overlap"])
    native_next = segment(next_track["input"], candidate["entry"], total)
    native_placed = padded(native_next, total, round(lead * RATE))
    equal_timing = equal_power(old, native_placed, times, lead, lead + candidate["overlap"])
    phrase, voice_report = make_phrase_mix(old_stems, processed, old, candidate, lead, total)
    if candidate["estimated_vocal_overlap"] > .1:
        raise ValueError("人声正文交叠，不接受该乐句候选")
    write_float(directory / "ordinary-5s.wav", ordinary)
    write_float(directory / "same-points-crossfade.wav", equal_timing)
    write_float(directory / "beat-matched-full-crossfade.wav", full_eq)
    write_float(directory / "phrase.wav", phrase)
    write_float(directory / "phrase-short.wav", phrase[:round(core_seconds * RATE)])
    write_float(directory / "old-original-stem.wav", old)
    write_float(directory / "next-original-stem.wav", next_stems.sum(axis=0))
    write_float(directory / "next-rendered-stem.wav", new_full)
    write_float(directory / "old-drums.wav", old_stems[0])
    write_float(directory / "next-native-drums.wav", next_stems[0])
    write_float(directory / "next-rendered-drums.wav", processed[0])
    write_float(directory / "old-vocals.wav", old_stems[3])
    write_float(directory / "next-rendered-vocals.wav", processed[3])
    report = {
        "candidate": candidate,
        "prototype": True,
        "clip_start_in_current": clip_start,
        "lead_seconds": lead,
        "overlap_start_seconds": lead,
        "overlap_end_seconds": lead + candidate["overlap"],
        "duration_seconds": seconds,
        "short_duration_seconds": core_seconds,
        "ordinary_next_starts_seconds": ordinary_start,
        "ordinary_next_vocal_estimated_seconds": ordinary_voice,
        "prototype_next_vocal_estimated_seconds": prototype_voice,
        "vocal_preservation": voice_report,
        "source_stem_restore_gains": {"current": old_gains, "next": new_gains},
        "peak_before_normalization": {
            "ordinary": float(np.max(np.abs(ordinary))),
            "phrase": float(np.max(np.abs(phrase))),
            "same_points": float(np.max(np.abs(equal_timing))),
        },
        "pitch_shift_semitones": 0,
        "tempo_recovery": "starts half a second after overlap, smoothstep to 1 over four native beats",
        "assumptions": [
            "section and vocal activity estimates are not human annotations",
            "only estimated non-vocal intro and outro material is skipped",
            "full-song offline analysis; not a real-time player implementation",
        ],
    }
    (directory / "report.json").write_text(json.dumps(report, ensure_ascii=False, indent=2), encoding="utf-8")
    print(json.dumps({
        "rendered": str(directory), "current": current["title"], "next": next_track["title"],
        "ordinary_voice": ordinary_voice, "prototype_voice": prototype_voice,
        "estimated_vocal_gap": candidate["estimated_vocal_gap"],
    }, ensure_ascii=True), flush=True)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--current")
    parser.add_argument("--next")
    parser.add_argument("--output", default="renders")
    parser.add_argument("--limit", type=int, default=2)
    arguments = parser.parse_args()
    library = {track["id"]: track for track in json.loads((ROOT / "library.json").read_text(encoding="utf-8"))}
    candidates = json.loads((ROOT / "phrase-candidates.json").read_text(encoding="utf-8"))["candidates"]
    selected = []
    pairs = set()
    for candidate in candidates:
        if arguments.current and arguments.current != candidate["current_id"]:
            continue
        if arguments.next and arguments.next != candidate["next_id"]:
            continue
        pair = (candidate["current_id"], candidate["next_id"])
        if pair in pairs:
            continue
        selected.append(candidate)
        pairs.add(pair)
        if arguments.current or len(selected) == arguments.limit:
            break
    if not selected:
        raise RuntimeError("没有满足安全乐句条件的曲对，不能强行生成过渡")
    for index, candidate in enumerate(selected, 1):
        render_candidate(candidate, library, ROOT / arguments.output / f"pair-{index:02d}")


if __name__ == "__main__":
    main()
