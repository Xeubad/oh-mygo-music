"""接收播放器本机解码的 WAV，输出通过门禁的混音与源时间映射。"""

import argparse
import hashlib
import json
import os
from pathlib import Path
import sys
import time
import shutil
import itertools

from engine_runtime import ROOT, RATE

import librosa
import numpy as np
import soundfile as sf
import torch
from beat_this.inference import Audio2Beats
from demucs_infer import DemucsSession
from scipy.ndimage import binary_closing, binary_opening

from adaptive_planner import plan_pair
from render import render_candidate
import spectral
from structure import structure

VERSION = "adaptive-1"
ANALYSIS_VERSION = "boundary-40-48-v1"
MODEL_HASHES = {
    "small0.ckpt": "6074be2c4d490c5f6101fcc374a1ec72ae93456e23bb6019783b849f5dc7d47b",
    "955717e8-8726e21a.th": "8726e21a993978c7ba086d3872e7608d7d5bfca646ca4aca459ffda844faa8b4",
}


def prune_analysis_cache(cache, protected):
    root = (cache / ANALYSIS_VERSION).resolve()
    root.mkdir(parents=True, exist_ok=True)
    entries = []
    deadline = time.monotonic() + 5
    for directory in itertools.islice(root.iterdir(), 64):
        if time.monotonic() > deadline:
            break
        if not directory.is_dir() or directory.is_symlink():
            continue
        if directory.parent.resolve() != root or len(directory.name.split("-")[0]) != 64:
            continue
        files = list(directory.glob("*.wav")) + list(directory.glob("*.npz"))
        files += list((directory / "stems").glob("*.wav"))
        size = sum(path.stat().st_size for path in files if path.is_file())
        entries.append((directory.stat().st_mtime, size, directory))
    total = sum(size for _, size, _ in entries)
    for _, size, directory in sorted(entries):
        if total <= 1 << 30:
            break
        if directory.name in protected:
            continue
        shutil.rmtree(directory)
        total -= size


def write_json(path, value):
    temporary = path.with_suffix(".part")
    temporary.write_text(json.dumps(value, ensure_ascii=False, allow_nan=False), encoding="utf-8")
    temporary.replace(path)


def fingerprint(path):
    digest = hashlib.sha256()
    with path.open("rb") as source:
        while chunk := source.read(1 << 20):
            digest.update(chunk)
    return digest.hexdigest()


def voice_regions(audio, voice, destination, origin):
    hop = RATE // 20
    count = min(len(audio), len(voice)) // hop
    mixture_rms = np.sqrt(np.mean(audio[:count*hop].reshape(count, hop, 2) ** 2, axis=(1, 2)))
    vocal_rms = np.sqrt(np.mean(voice[:count*hop].reshape(count, hop, 2) ** 2, axis=(1, 2)))
    relative = 20 * np.log10(np.maximum(vocal_rms, 1e-8) / np.maximum(mixture_rms, 1e-8))
    active = (20 * np.log10(np.maximum(vocal_rms, 1e-8)) > -38) & (relative > -20)
    active = binary_opening(binary_closing(active, structure=np.ones(5)), structure=np.ones(3))
    changes = np.diff(np.r_[False, active, False].astype(int))
    regions = [
        {"start": float(origin + first * .05), "end": float(origin + last * .05)}
        for first, last in zip(np.flatnonzero(changes == 1), np.flatnonzero(changes == -1))
        if last - first >= 4
    ]
    np.savez_compressed(destination, times=origin + np.arange(count) * .05, mixture_rms=mixture_rms)
    return regions


def analyze(path, role, cache, tracker, separator):
    key = fingerprint(path)
    directory = cache / ANALYSIS_VERSION / f"{key}-{role}"
    directory.mkdir(parents=True, exist_ok=True)
    metadata = directory / "features.json"
    if metadata.exists():
        result = json.loads(metadata.read_text(encoding="utf-8"))
        if result.get("complete") and all(Path(result[name]).exists() for name in ("input", "voice_features")):
            return result
    audio, rate = sf.read(path, dtype="float32", always_2d=True)
    if audio.shape[1] != 2 or rate != 48000 or len(audio) > 1200 * rate or not np.isfinite(audio).all():
        raise ValueError("播放器音频格式或长度不可信")
    original_frames = len(audio)
    audio = librosa.resample(audio.T, orig_sr=rate, target_sr=RATE).T
    source = directory / "source.wav"
    sf.write(source, audio, RATE, subtype="FLOAT")
    duration = len(audio) / RATE
    # 边界分析有固定上限；歌曲中间内容不参与截断，出口始终还原到完整源时间轴。
    first = max(0, len(audio) - 40 * RATE) if role == "outgoing" else 0
    last = len(audio) if role == "outgoing" else min(len(audio), 48 * RATE)
    excerpt = audio[first:last]
    origin = first / RATE
    if len(excerpt) < 10 * RATE:
        raise ValueError("音频太短，保留原曲接续")
    torch.manual_seed(42)
    _, separated = separator.infer(torch.from_numpy(excerpt.T.copy()), sample_rate=RATE)
    stems = directory / "stems"
    stems.mkdir(exist_ok=True)
    for name, values in separated.items():
        # 保存完整时间轴，窗外的补零只能作为存储，不得被视为有效分析范围。
        samples = np.zeros_like(audio)
        values = values.detach().cpu().numpy().T
        samples[first:first + min(len(excerpt), len(values))] = values[:len(excerpt)]
        sf.write(stems / f"{name}.wav", samples, RATE, subtype="FLOAT")
    vocals, _ = sf.read(stems / "vocals.wav", dtype="float32", always_2d=True)
    voice_path = directory / "voice.npz"
    regions = voice_regions(excerpt, vocals[first:last], voice_path, origin)
    beats, downbeats = tracker(excerpt, RATE)
    segments, _ = structure(excerpt, RATE, np.asarray(downbeats), spectral)
    for segment in segments:
        segment["start"] += origin
        segment["end"] += origin
    result = {
        "id": key, "title": role, "duration": duration, "input": str(source),
        "stems": str(stems), "voice_features": str(voice_path),
        "beats": (np.asarray(beats) + origin).tolist(),
        "downbeats": (np.asarray(downbeats) + origin).tolist(),
        "segments": segments, "vocal_regions_estimated": regions,
        "analysis_start": origin, "analysis_end": last / RATE,
        "original_frames": original_frames, "complete": True,
    }
    write_json(metadata, result)
    return result


def main():
    started = time.perf_counter()
    parser = argparse.ArgumentParser()
    parser.add_argument("--job", type=Path)
    parser.add_argument("--self-test", action="store_true")
    arguments = parser.parse_args()
    torch.set_num_threads(min(4, os.cpu_count() or 1))
    torch.set_num_interop_threads(2)
    models = Path(os.environ.get("MYGO_AUTOMIX_MODELS", str(ROOT / "models")))
    for name, expected in MODEL_HASHES.items():
        if fingerprint(models / name) != expected:
            raise ValueError("本机模型校验失败")
    device = os.environ.get("MYGO_AUTOMIX_DEVICE", "cpu")
    if device == "cuda":
        torch.cuda.set_per_process_memory_fraction(.20)
    tracker = Audio2Beats(checkpoint_path=str(models / "small0.ckpt"), device=device, dbn=False)
    if arguments.self_test:
        print(json.dumps({"schema": VERSION, "status": "ready", "device": device}))
        return
    job = json.loads(arguments.job.read_text(encoding="utf-8"))
    output = Path(job["output"])
    output.mkdir(parents=True, exist_ok=True)
    response = output / "result.json"
    if job.get("schema") != VERSION or not .01 <= job.get("gain_ratio", 0) <= 100:
        raise ValueError("无效的播放器任务")
    separator = DemucsSession(
        checkpoint_path=models / "955717e8-8726e21a.th",
        device=device, shifts=1, overlap=.25, jobs=0, progress=False,
    ).load()
    cache = Path(job["cache"])
    protected = {fingerprint(Path(job["current"])) + "-outgoing", fingerprint(Path(job["next"])) + "-incoming"}
    prune_analysis_cache(cache, protected)
    current = analyze(Path(job["current"]), "outgoing", cache, tracker, separator)
    print(f"outgoing_analysis_ready:{time.perf_counter()-started:.2f}s", flush=True)
    incoming = analyze(Path(job["next"]), "incoming", cache, tracker, separator)
    print(f"incoming_analysis_ready:{time.perf_counter()-started:.2f}s", flush=True)
    separator.close()
    from envelopes import segment

    profiles = {}

    def profile(track, start, end):
        key = track["id"], start, end
        if key not in profiles:
            samples = segment(track["input"], start, round((end-start) * RATE))
            chroma = librosa.feature.chroma_stft(y=samples.mean(axis=1)[::2], sr=RATE//2,
                                               n_fft=4096, hop_length=2048, tuning=0).mean(axis=1)
            profiles[key] = chroma / max(float(np.linalg.norm(chroma)), 1e-12)
        return profiles[key]

    def power(track, start, end):
        with np.load(track["voice_features"]) as values:
            selected = (values["times"] >= start) & (values["times"] < end)
            return float(np.mean(values["mixture_rms"][selected] ** 2)) if np.any(selected) else 0

    plans = plan_pair(current, incoming, profile=profile, power=power)
    print(f"safe_phrase_candidates:{len(plans['candidates'])}:{time.perf_counter()-started:.2f}s", flush=True)
    preferred = None
    if job.get("advisor") and plans["candidates"]:
        offered = plans["candidates"][:16]
        # 云端只见聚合节奏和本机候选，不含 PCM、源路径、歌曲标识或任何来源凭据。
        request = {
            "schema": "ohmygo.automix.features.v1", "sample_rate": 48000,
            "current": {"start_frame": round(current["analysis_start"]*48000),
                        "end_frame": current["original_frames"], "vocal_activity": True,
                        "tempo": {"bpm": offered[0]["current_tempo"]["bpm"], "confidence": 1}},
            "next": {"start_frame": 0, "end_frame": round(incoming["analysis_end"]*48000),
                     "vocal_activity": True,
                     "tempo": {"bpm": offered[0]["next_tempo"]["bpm"], "confidence": 1}},
            "candidates": [
                {"id": f"mix-{index+1}", "start_frame": round(candidate["mix_start"]*48000),
                 "end_frame": round(candidate["mix_end"]*48000), "entry_frame": round(candidate["entry"]*48000),
                 "speed_ratio": candidate["ratio"], "harmony_score": candidate["actual_chroma_similarity"],
                 "bar_aligned": True, "local_score": candidate["score"], "profiles": ["matched"]}
                for index, candidate in enumerate(offered)
            ],
        }
        write_json(output / "request.json", request)
        deadline = time.monotonic() + 3.5
        while time.monotonic() < deadline:
            decision_path = output / "decision.json"
            if decision_path.exists():
                decision = json.loads(decision_path.read_text(encoding="utf-8"))
                allowed = {f"mix-{index+1}": candidate for index, candidate in enumerate(offered)}
                if decision.get("profile") == "matched":
                    preferred = allowed.get(decision.get("candidate_id"))
                break
            time.sleep(.05)
    candidates = plans["candidates"][:20]
    if preferred is not None:
        candidates = [preferred] + [candidate for candidate in candidates if candidate != preferred]
    attempts = []
    for index, candidate in enumerate(candidates):
        if candidate["mix_start"] < job.get("earliest", 0):
            continue
        if candidate["mix_start"] - 6 < current["analysis_start"] or candidate["entry"] < incoming["analysis_start"]:
            continue
        required_next = candidate["entry"] + (candidate["overlap"] + 12) * max(candidate["ratio"], 1)
        if required_next > incoming["analysis_end"]:
            continue
        directory = output / f"attempt-{index+1:02d}"
        print(f"render_candidate:{index+1}:{candidate['outgoing_beats']}:{time.perf_counter()-started:.2f}s", flush=True)
        try:
            report = render_candidate(candidate, current, incoming, directory, gain_ratio=job["gain_ratio"])
        except Exception as error:
            print(f"render_failed:{index+1}:{type(error).__name__}:{error}", flush=True)
            attempts.append({"rank": index+1, "status": "render_failed", "error": str(error)})
            continue
        attempts.append({"rank": index+1, "status": report["status"],
                         "failed_checks": [name for name, passed in report["checks"].items() if not passed]})
        if report["status"] != "accepted":
            continue
        samples, _ = sf.read(directory / "phrase.wav", dtype="float32", always_2d=True)
        lead = report["lead_seconds"]
        samples = samples[round(lead * RATE):]
        samples = librosa.resample(samples.T, orig_sr=RATE, target_sr=48000).T
        mixed_path = output / "mixed.wav"
        sf.write(mixed_path, samples, 48000, subtype="FLOAT")
        source = np.asarray(report["source_map_seconds"])
        target = np.asarray(report["target_map_seconds"])
        end = len(samples) / 48000
        times = np.unique(np.r_[0, target[(target > 0) & (target < end)], end])
        mapping = [
            {"Output": round(at * 48000),
             "Source": round((float(np.interp(at, target, source)) + candidate["entry"]) * 48000)}
            for at in times
        ]
        result = {
            "schema": VERSION, "status": "accepted", "checks": report["checks"], "attempts": attempts,
            "ai_selected": preferred is not None and candidate == preferred,
            "start": round(candidate["mix_start"] * 48000), "end": round(candidate["mix_end"] * 48000),
            "entry": round(candidate["entry"] * 48000), "beats": candidate["outgoing_beats"],
            "incoming_beats": candidate["incoming_beats"], "ratio": candidate["ratio"],
            "current_frames": current["original_frames"], "next_frames": incoming["original_frames"],
            "mixed": str(mixed_path), "mapping": mapping,
            "consumed": mapping[-1]["Source"], "quality": report,
        }
        write_json(response, result)
        prune_analysis_cache(cache, protected)
        return
    write_json(response, {"schema": VERSION, "status": "rejected", "attempts": attempts,
                          "reason": plans["reason"]})


if __name__ == "__main__":
    main()
