"""许可已核对的本机结构替代原型：重算拍点与结构，读取音乐前拒绝网络。"""

import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import sys
import time

ROOT = Path(__file__).resolve().parent / "clean-structure"
BASE = Path(__file__).resolve().parent
for variable in ("OMP_NUM_THREADS", "MKL_NUM_THREADS", "OPENBLAS_NUM_THREADS", "NUMBA_NUM_THREADS"):
    os.environ[variable] = "2"
os.environ["HF_HUB_OFFLINE"] = "1"
os.environ["TRANSFORMERS_OFFLINE"] = "1"
os.environ["HF_HUB_DISABLE_TELEMETRY"] = "1"
os.environ["DO_NOT_TRACK"] = "1"
os.environ["MPLBACKEND"] = "Agg"

import librosa
import numpy as np
import scipy.ndimage
import soundfile as sf
import torch
from beat_this.inference import Audio2Beats
from sklearn.cluster import KMeans


def deny_network(event, arguments):
    if event in {"socket.connect", "socket.getaddrinfo", "socket.sendto"}:
        raise RuntimeError("读取用户音乐前已禁用网络，禁止音频分析阶段连接外部服务")


def read_module(name, path):
    specification = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(specification)
    specification.loader.exec_module(module)
    return module


def write_json(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2), encoding="utf-8")


def normalize_columns(features):
    return features / np.maximum(np.linalg.norm(features, axis=0, keepdims=True), 1e-9)


def structure(audio, rate, downbeats, spectral):
    mono = librosa.resample(audio.mean(axis=1), orig_sr=rate, target_sr=22050)
    hop = 1024
    magnitude = np.abs(librosa.stft(mono, n_fft=4096, hop_length=hop))
    power = magnitude ** 2
    chroma = librosa.feature.chroma_stft(S=power, sr=22050, n_fft=4096, tuning=0)
    mel = librosa.feature.melspectrogram(S=power, sr=22050, n_mels=80)
    mfcc = librosa.feature.mfcc(S=librosa.power_to_db(mel), n_mfcc=20)
    duration = len(audio) / rate
    # 小节同步保留完整首尾；特征列 i 严格对应边界 i 到 i+1，避免接歌点偏移一小节。
    frames = np.unique(np.r_[0, librosa.time_to_frames(
        downbeats, sr=22050, hop_length=hop
    ), magnitude.shape[1]])
    frames = frames[(frames >= 0) & (frames <= magnitude.shape[1])]
    chroma_sync = librosa.util.sync(chroma, frames, aggregate=np.median, pad=False)
    mfcc_sync = librosa.util.sync(mfcc, frames, aggregate=np.mean, pad=False)
    boundary_times = np.minimum(frames * hop / 22050, duration)
    boundary_times[-1] = duration
    assert chroma_sync.shape[1] == len(boundary_times) - 1
    mfcc_sync = (mfcc_sync - mfcc_sync.mean(axis=1, keepdims=True)) / np.maximum(
        mfcc_sync.std(axis=1, keepdims=True), 1
    )
    config = {"rec_width": 3, "rec_smooth": 3, "evec_smooth": 3}
    embedding = spectral.embed_beats(normalize_columns(chroma_sync), mfcc_sync, config)
    cluster_count = min(6, max(3, round(duration / 50)))
    denominator = np.maximum(np.linalg.norm(embedding[:, :cluster_count], axis=1, keepdims=True), 1e-9)
    labels = KMeans(
        n_clusters=cluster_count, n_init=20, max_iter=300, random_state=42
    ).fit_predict(embedding[:, :cluster_count] / denominator)
    # 聚类编号没有数值顺序；用相邻一致分组消除孤立的小节。
    for index in range(1, len(labels) - 1):
        if labels[index - 1] == labels[index + 1]:
            labels[index] = labels[index - 1]
    changes = np.r_[0, 1 + np.flatnonzero(np.diff(labels)), len(labels)]
    segments = []
    for start, stop in zip(changes[:-1], changes[1:]):
        segments.append({
            "start": float(boundary_times[start]),
            "end": float(boundary_times[stop]),
            "label": f"section_{int(labels[start]) + 1}",
            "repetition_group": int(labels[start]),
            "semantic_label": None,
        })
    assert segments[0]["start"] == 0 and segments[-1]["end"] == duration
    assert all(segment["end"] > segment["start"] for segment in segments)
    # 输出可核验的重复结构；不把无监督分组冒称为主歌或副歌。
    similarity = normalize_columns(chroma_sync).T @ normalize_columns(chroma_sync)
    return segments, {
        "bar_times": boundary_times,
        "bar_labels": labels,
        "self_similarity": similarity.astype(np.float32),
        "spectral_embedding": embedding[:, :cluster_count].astype(np.float32),
    }


def compare_boundaries(old_segments, new_segments):
    old = np.asarray([segment["start"] for segment in old_segments[1:]], dtype=float)
    new = np.asarray([segment["start"] for segment in new_segments[1:]], dtype=float)
    if not len(old) or not len(new):
        return {"supported": False}
    old_to_new = np.min(np.abs(old[:, None] - new[None, :]), axis=1)
    new_to_old = np.min(np.abs(new[:, None] - old[None, :]), axis=1)
    return {
        "supported": True,
        "old_boundary_count": len(old),
        "new_boundary_count": len(new),
        "old_to_new_median_seconds": float(np.median(old_to_new)),
        "new_to_old_median_seconds": float(np.median(new_to_old)),
        "old_to_new_within_3s_fraction": float(np.mean(old_to_new <= 3)),
        "reference": "previous All-In-One estimates, NOT human ground truth",
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--limit", type=int, default=10)
    arguments = parser.parse_args()
    audit = json.loads((ROOT / "license-audit.json").read_text(encoding="utf-8"))
    assert audit["decisions"]["SongFormer"].startswith("rejected")
    checkpoint = BASE / "models/torch/hub/checkpoints/beat_this-small0.ckpt"
    assert hashlib.sha1(checkpoint.read_bytes()).hexdigest() == "77a7ef5c21f628578f2b259ac29d2d680412efcc"
    torch.set_num_threads(2)
    torch.set_num_interop_threads(2)
    assert torch.cuda.is_available(), "应使用已恢复的 CUDA 环境"
    torch.cuda.set_per_process_memory_fraction(.20)
    tracker = Audio2Beats(checkpoint_path=str(checkpoint), device="cuda", dbn=False)
    source = Path(sys.prefix) / "Lib/site-packages/msaf/algorithms/scluster/main2.py"
    spectral = read_module("licensed_msaf_spectral", source)
    planner = read_module("prototype_phrase_rules", BASE / "plan_phrases.py")
    planner.local_tempo = stable_local_tempo
    sys.addaudithook(deny_network)
    try:
        import socket
        socket.getaddrinfo("example.invalid", 443)
        raise AssertionError("离线保护未生效")
    except RuntimeError:
        pass
    previous = json.loads((BASE / "library.json").read_text(encoding="utf-8"))[:arguments.limit]
    library = []
    comparisons = []
    for track in previous:
        destination = ROOT / "analysis" / f"{track['id']}.json"
        if destination.exists():
            result = json.loads(destination.read_text(encoding="utf-8"))
        else:
            started = time.perf_counter()
            audio, rate = sf.read(track["input"], dtype="float32", always_2d=True)
            beats, downbeats = tracker(audio, rate)
            segments, arrays = structure(audio, rate, np.asarray(downbeats), spectral)
            # 只继承音源、独立 HTDemucs 人声测量；不继承受限模型的拍点或段落预测。
            fields = ("id", "title", "artist", "input", "stems", "voice_features",
                      "duration", "vocal_regions_estimated")
            result = {field: track[field] for field in fields}
            result.update({
                "beats": np.asarray(beats).tolist(),
                "downbeats": np.asarray(downbeats).tolist(),
                "bpm": float(60 / np.median(np.diff(beats))),
                "segments": segments,
                "analysis_seconds": time.perf_counter() - started,
                "backend": "Beat This small0 MIT + MSAF ISC spectral clustering, no structure weights",
                "audio_network_policy": "socket/DNS denied before reading music",
            })
            write_json(destination, result)
            np.savez_compressed(destination.with_suffix(".npz"), **arrays)
        library.append(result)
        comparison = {
            "id": track["id"], "title": track["title"],
            "old_segments": track["segments"], "new_segments": result["segments"],
            "boundary_agreement": compare_boundaries(track["segments"], result["segments"]),
            "analysis_seconds": result["analysis_seconds"],
        }
        comparisons.append(comparison)
        print(json.dumps({
            "analyzed": len(library), "id": track["id"],
            "segments": len(result["segments"]),
            "seconds": round(result["analysis_seconds"], 2),
        }), flush=True)
    write_json(ROOT / "library.json", library)
    write_json(ROOT / "structure-comparison.json", comparisons)
    # 缓存累计能量，避免枚举曲对时反复解压同一首歌的特征文件。
    power_cache = {}

    def cached_power(track, start, end):
        key = track["id"]
        if key not in power_cache:
            with np.load(track["voice_features"]) as features:
                times = features["times"].copy()
                cumulative = np.r_[0, np.cumsum(features["mixture_rms"] ** 2)]
            power_cache[key] = times, cumulative
        times, cumulative = power_cache[key]
        first, last = np.searchsorted(times, [start, end], side="left")
        return float((cumulative[last] - cumulative[first]) / (last - first)) if last > first else 0

    planner.window_power = cached_power
    candidates = []
    rejected = []
    for current in library:
        for incoming in library:
            if current["id"] == incoming["id"]:
                continue
            found, reason = planner.candidates_for_pair(current, incoming)
            if not found:
                rejected.append({"current_id": current["id"], "next_id": incoming["id"], "reason": reason})
                continue
            for candidate in sorted(found, key=lambda row: row["score"])[:3]:
                first = planner.spectral_profile(current, candidate["mix_start"], candidate["mix_end"])
                second = planner.spectral_profile(incoming, candidate["entry"],
                    candidate["entry"] + candidate["overlap"] * candidate["ratio"])
                similarity = float(first @ second)
                if similarity < .65:
                    continue
                candidate["actual_chroma_similarity"] = similarity
                candidate["score"] += (1 - similarity) * 2
                candidate["structure_backend"] = "MSAF ISC spectral; no semantic chorus claims"
                candidates.append(candidate)
    candidates.sort(key=lambda item: item["score"])
    accepted_pairs = {(item["current_id"], item["next_id"]) for item in candidates}
    write_json(ROOT / "phrase-candidates.json", {
        "candidates": candidates,
        "rejected_before_chroma": rejected,
        "accepted_unique_pairs": len(accepted_pairs),
        "rejected_unique_pairs": len(library) * (len(library) - 1) - len(accepted_pairs),
    })
    print(json.dumps({
        "stage": "CLEAN_ANALYSIS_DONE", "tracks": len(library),
        "candidates": len(candidates), "unique_pairs": len(accepted_pairs),
        "median_analysis_seconds": float(np.median([item["analysis_seconds"] for item in library])),
    }), flush=True)


if __name__ == "__main__":
    main()
