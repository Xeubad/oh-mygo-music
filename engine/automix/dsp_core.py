"""从 automix-engine 提取的逐拍变速、原子发布和实际鼓点门禁，保留 LGPL 许可。"""

import hashlib
import json
import math
import os
from pathlib import Path
import subprocess
import tempfile

import librosa
import numpy as np
import soundfile as sf

SR = 44100
PULSES_PER_BAR = 4
MAX_RELEASE_PULSES = 16
RELEASE_CONTROL_POINTS = 16
TEMPO_RELEASE_SECONDS = 6.48


def sha256_file(path):
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for block in iter(lambda: source.read(1 << 20), b""):
            digest.update(block)
    return digest.hexdigest()


def atomic_write_audio(destination, audio, subtype):
    destination.parent.mkdir(parents=True, exist_ok=True)
    handle, name = tempfile.mkstemp(prefix=f".{destination.stem}.", suffix=destination.suffix, dir=destination.parent)
    os.close(handle)
    temporary = Path(name)
    try:
        sf.write(temporary, audio, SR, subtype=subtype)
        temporary.replace(destination)
    finally:
        temporary.unlink(missing_ok=True)


def atomic_write_text(destination, content):
    destination.parent.mkdir(parents=True, exist_ok=True)
    handle, name = tempfile.mkstemp(prefix=f".{destination.name}.", dir=destination.parent)
    temporary = Path(name)
    try:
        with os.fdopen(handle, "w", encoding="utf-8") as output:
            output.write(content)
            output.flush()
            os.fsync(output.fileno())
        temporary.replace(destination)
    finally:
        temporary.unlink(missing_ok=True)


def audio_file_matches(path, expected_frames):
    if not path.exists():
        return False
    try:
        info = sf.info(path)
    except (OSError, RuntimeError):
        return False
    return (info.format == "WAV" and info.subtype == "FLOAT" and info.samplerate == SR
            and info.channels == 2 and info.frames == expected_frames)


def rubberband_audio(source, destination, time_map, output_duration):
    expected_frames = round(output_duration * SR)
    descriptor = {
        "version": "rubberband-4-r3-fine-timemap-v1",
        "source": str(source.resolve()), "source_size": source.stat().st_size,
        "source_mtime_ns": source.stat().st_mtime_ns, "time_map_sha256": sha256_file(time_map),
        "output_duration": output_duration, "expected_frames": expected_frames,
    }
    descriptor_path = destination.with_suffix(destination.suffix + ".cache.json")
    if audio_file_matches(destination, expected_frames) and descriptor_path.exists():
        try:
            if json.loads(descriptor_path.read_text(encoding="utf-8")) == descriptor:
                return
        except (OSError, json.JSONDecodeError):
            pass
    destination.parent.mkdir(parents=True, exist_ok=True)
    handle, name = tempfile.mkstemp(prefix=f".{destination.stem}.", suffix=destination.suffix, dir=destination.parent)
    os.close(handle)
    temporary = Path(name)
    try:
        subprocess.run([
            "rubberband", "--quiet", "--fine", "--ignore-clipping", "--timemap", str(time_map),
            "--duration", f"{output_duration:.12f}", str(source), str(temporary),
        ], check=True, creationflags=subprocess.CREATE_NO_WINDOW if os.name == "nt" else 0)
        if not audio_file_matches(temporary, expected_frames):
            raise RuntimeError("Rubber Band returned invalid audio")
        temporary.replace(destination)
        atomic_write_text(descriptor_path, json.dumps(descriptor))
    finally:
        temporary.unlink(missing_ok=True)


def build_tempo_map(report, source_wav, rhythm_timing, cue_refinement, *, map_path):
    offsets = np.asarray(rhythm_timing["target_offsets_seconds"], dtype=float)
    count = rhythm_timing.get("transition_pulses", len(offsets)-1)
    if not isinstance(count, int) or not 2 <= count <= 64 or len(offsets) != count+1:
        raise ValueError("Invalid adaptive pulse count")
    if not np.isfinite(offsets).all() or offsets[0] != 0 or np.any(np.diff(offsets) <= 0):
        raise ValueError("Invalid target pulse grid")
    incoming_bpm = float(cue_refinement["incoming_pulse_bpm"])
    cue_pulse = int(rhythm_timing["incoming_cue_pulse"])
    if not 0 <= cue_pulse <= count:
        raise ValueError("Cue outside adaptive phrase")
    beats = np.asarray(cue_refinement["incoming_beat_times_seconds"], dtype=float)
    if not np.isfinite(beats).all() or np.any(np.diff(beats) <= 0):
        raise ValueError("Invalid incoming pulse grid")
    cue_index = int(np.argmin(abs(beats - cue_refinement["refined_cue_seconds"])))
    start, stop = cue_index-cue_pulse, cue_index+(count-cue_pulse)
    if start < 0 or stop+MAX_RELEASE_PULSES >= len(beats):
        raise ValueError("Insufficient tempo lock and release material")
    period = float(rhythm_timing["target_pulse_interval_seconds"])
    initial_rate = float(rhythm_timing["pulse_bpm"]) / incoming_bpm
    source_start, source_end = float(beats[start]), float(beats[stop])
    target_start = source_start / initial_rate
    points = [(0.0, 0.0)]
    # 每一对真实拍点都锁定，入口在零时只记录一次，时间映射保持严格单调。
    for step, index in enumerate(range(start, stop+1)):
        point = float(beats[index]), target_start + float(offsets[step])
        if point[0] == points[-1][0]:
            points[-1] = point
        else:
            points.append(point)
    native_period = float(cue_refinement["incoming_pulse_interval_seconds"])
    matched_rate = native_period / period
    release = float(rhythm_timing.get("tempo_release_seconds", TEMPO_RELEASE_SECONDS))
    if not math.isfinite(release) or not .5 <= release <= 12:
        raise ValueError("Invalid tempo recovery duration")
    interval = release / RELEASE_CONTROL_POINTS
    current_source, current_target = source_end, points[-1][1]
    previous_rate = matched_rate
    release_points = []
    # 积分连续速度而不是强迫落在整数拍上，防止恢复原速时倒退或突然跳时。
    for step in range(1, RELEASE_CONTROL_POINTS+1):
        progress = step / RELEASE_CONTROL_POINTS
        eased = progress * progress * (3-2*progress)
        rate = matched_rate + (1-matched_rate)*eased
        segment_rate = .5 * (previous_rate+rate)
        current_source += interval * segment_rate
        current_target += interval
        points.append((current_source, current_target))
        release_points.append({
            "control_point": step, "source_seconds": current_source, "target_seconds": current_target,
            "playback_rate": rate, "segment_playback_rate": segment_rate,
            "target_pulse_bpm": incoming_bpm*rate,
        })
        previous_rate = rate
    source_frames = sf.info(source_wav).frames
    duration = source_frames / SR
    release_source_end, release_target_end = points[-1]
    if release_source_end >= duration:
        raise ValueError("Tempo recovery exceeds decoded audio")
    output_duration = release_target_end + duration-release_source_end
    points.append((duration, output_duration))
    frame_points = []
    for source, target in points:
        pair = round(source*SR), round(target*SR)
        if 0 < pair[0] < source_frames and (not frame_points or pair[0] > frame_points[-1][0]):
            frame_points.append(pair)
    atomic_write_text(map_path, "".join(f"{source} {target}\n" for source, target in frame_points))
    sources = np.asarray([point[0] for point in points])
    targets = np.asarray([point[1] for point in points])
    return {
        "path": map_path, "output_duration": output_duration,
        "source_seconds": sources, "target_seconds": targets,
        "transition_start_target": float(np.interp(source_start, sources, targets)),
        "transition_end_source": source_end, "transition_end_target": float(np.interp(source_end, sources, targets)),
        "cue_target": target_start+float(offsets[cue_pulse]),
        "release_source_end": release_source_end, "release_target_end": release_target_end,
        "release_points": release_points, "release_control_points": RELEASE_CONTROL_POINTS,
        "frame_points": frame_points,
    }


def drum_grid_qa(audio, target_offsets, subdivision):
    mono = np.mean(audio, axis=0)
    onset = librosa.onset.onset_strength(y=mono, sr=SR, hop_length=128, aggregate=np.median)
    times = librosa.times_like(onset, sr=SR, hop_length=128)
    peaks = librosa.util.peak_pick(onset, pre_max=4, post_max=4, pre_avg=12, post_avg=12, delta=.15, wait=7)
    transition = float(target_offsets[-1])
    start = float(target_offsets[min(PULSES_PER_BAR, len(target_offsets)-2)])
    peaks = peaks[(times[peaks] >= start) & (times[peaks] <= transition-.2)]
    if len(peaks):
        peaks = peaks[onset[peaks] >= np.percentile(onset[peaks], 35)]
    minimum_span = 2 * float(np.median(np.diff(target_offsets)))
    if len(peaks) < 8 or times[peaks[-1]]-times[peaks[0]] < minimum_span:
        return {"attack_count": int(len(peaks)), "passed": False,
                "reason": "insufficient rendered attacks or temporal coverage"}
    grid = []
    for first, last in zip(target_offsets, target_offsets[1:]):
        grid.extend(first+(last-first)*step/subdivision for step in range(subdivision))
    grid.append(transition)
    grid = np.asarray(grid)
    nearest = np.argmin(abs(times[peaks, None]-grid[None, :]), axis=1)
    error = times[peaks]-grid[nearest]
    period = float(np.median(np.diff(grid)))
    intervals = np.diff(times[peaks])
    intervals = intervals[(intervals >= .08) & (intervals <= 2)]
    measured = float(60/np.median(intervals)) if len(intervals) else 60/period
    p90 = float(np.percentile(abs(error), 90)*1000)
    return {
        "attack_count": int(len(peaks)), "analysis_start_seconds": start,
        "analysis_end_seconds": transition-.2, "drum_subdivision": subdivision,
        "measured_pulse_bpm": measured, "measured_pulse_method": "median rendered attack spacing",
        "median_signed_phase_ms": float(np.median(error)*1000),
        "median_absolute_phase_ms": float(np.median(abs(error))*1000),
        "p90_absolute_phase_ms": p90, "passed": bool(p90 <= .32*period*1000),
    }
