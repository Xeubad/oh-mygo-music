"""两首本机特征进入，完整可渲染的自适应乐句计划输出。"""

import math
from collections import Counter

import numpy as np


def _valid_track(track):
    try:
        if not isinstance(track.get("id"), str) or not track["id"] or not isinstance(track.get("title"), str):
            return False
        if not isinstance(track["duration"], (int, float)) or isinstance(track["duration"], bool):
            return False
        duration = float(track["duration"])
        if not math.isfinite(duration) or duration <= 0:
            return False
        for key in ("beats", "downbeats"):
            if any(not isinstance(value, (int, float)) or isinstance(value, bool) for value in track[key]):
                return False
            times = np.asarray(track[key], dtype=float)
            if len(times) < 9 or not np.isfinite(times).all():
                return False
            if times[0] < 0 or times[-1] > duration + .05 or np.any(np.diff(times) <= 0):
                return False
        previous = 0.0
        for region in track["vocal_regions_estimated"]:
            if any(not isinstance(region[key], (int, float)) or isinstance(region[key], bool)
                   for key in ("start", "end")):
                return False
            start, stop = float(region["start"]), float(region["end"])
            if not math.isfinite(start + stop) or not previous <= start < stop <= duration:
                return False
            previous = stop
        for segment in track.get("segments", []):
            start, stop = segment["start"], segment["end"]
            if not math.isfinite(start + stop) or not 0 <= start < stop <= duration + .05:
                return False
        return True
    except (KeyError, TypeError, ValueError):
        return False


def _meter(track):
    beats = np.asarray(track["beats"])
    bars = np.asarray(track["downbeats"])
    sizes = [
        int(np.count_nonzero((beats >= start - .04) & (beats < end - .04)))
        for start, end in zip(bars[:-1], bars[1:])
    ]
    meter, count = Counter(sizes).most_common(1)[0]
    return meter if 2 <= meter <= 6 and count / len(sizes) >= .65 else None


def _grid(track, first_bar, bars, meter):
    downbeats = np.asarray(track["downbeats"], dtype=float)
    beats = np.asarray(track["beats"], dtype=float)
    if first_bar < 0 or first_bar + bars >= len(downbeats):
        return None
    anchors = downbeats[first_bar:first_bar + bars + 1]
    expected = np.r_[np.concatenate([
        np.linspace(start, end, meter, endpoint=False)
        for start, end in zip(anchors[:-1], anchors[1:])
    ]), anchors[-1]]
    indices = np.asarray([np.argmin(abs(beats - at)) for at in expected])
    actual = beats[indices]
    period = float((actual[-1] - actual[0]) / (len(actual) - 1))
    if period <= 0 or np.any(np.diff(indices) <= 0):
        return None
    error = float(np.max(abs(actual - expected)) / period)
    bar_error = float(np.max(abs(np.diff(anchors) / meter - period)) / period)
    if error > .20 or bar_error > .12:
        return None
    return actual, {
        "bpm": 60 / period, "period": period, "meter": meter,
        "jitter": max(error, bar_error),
        "excluded_spurious_beats": int(np.count_nonzero(
            (beats >= actual[0]) & (beats <= actual[-1])
        ) - len(actual)),
    }


def _boundary_distance(track, at):
    return min((abs(at - segment["start"]) for segment in track.get("segments", [])), default=0)


def plan_pair(current, incoming, profile=None, power=None):
    """返回排序后的安全候选；拒绝也给明确原因，不静默退成固定时长淡化。"""
    if not _valid_track(current) or not _valid_track(incoming) or current["id"] == incoming["id"]:
        return {"status": "rejected", "reason": "invalid_features", "candidates": []}
    old_meter, new_meter = _meter(current), _meter(incoming)
    old_regions, new_regions = current["vocal_regions_estimated"], incoming["vocal_regions_estimated"]
    if not old_meter or not new_meter:
        return {"status": "rejected", "reason": "unstable_meter", "candidates": []}
    if not old_regions or not new_regions:
        return {"status": "rejected", "reason": "no_reliable_vocal_handoff", "candidates": []}
    # 稀少的分轨残留不足以证明存在演唱正文，宁可拒绝接唱计划也不虚构人声入口。
    if any(sum(region["end"] - region["start"] for region in track["vocal_regions_estimated"])
           < max(3, track["duration"] * .02) for track in (current, incoming)):
        return {"status": "rejected", "reason": "no_reliable_vocal_handoff", "candidates": []}
    last_voice, first_voice = old_regions[-1]["end"], new_regions[0]["start"]
    results = []
    reasons = Counter()
    grid_cache = {}

    def grid_for(track, first_bar, bars, meter):
        key = track["id"], first_bar, bars, meter
        if key not in grid_cache:
            grid_cache[key] = _grid(track, first_bar, bars, meter)
        return grid_cache[key]

    for exit_index, exit_time in enumerate(current["downbeats"]):
        if not last_voice + .25 <= exit_time <= current["duration"] - .1:
            continue
        if current["duration"] - exit_time > 30:
            continue
        for bars in (2, 4, 6, 8):
            old_grid = grid_for(current, exit_index - bars, bars, old_meter)
            if old_grid is None:
                continue
            old_beats, old_tempo = old_grid
            start, end = float(old_beats[0]), float(old_beats[-1])
            overlap = end - start
            if not 2.5 <= overlap <= 22 or start < current["duration"] - 50:
                continue
            for entry_index, entry in enumerate(incoming["downbeats"]):
                if entry > min(35, first_voice - .35):
                    break
                for scale in (.5, 1, 2):
                    old_count = len(old_beats) - 1
                    new_count = round(old_count * scale)
                    if new_count % new_meter:
                        continue
                    new_grid = grid_for(incoming, entry_index, new_count // new_meter, new_meter)
                    if new_grid is None:
                        continue
                    new_beats, new_tempo = new_grid
                    ratio = float((new_beats[-1] - new_beats[0]) / overlap)
                    if not .94 <= ratio <= 1.06:
                        reasons["tempo_mismatch"] += 1
                        continue
                    # 节拍层级不同只取已有拍点的子序列；不合成伪拍，也不把歌变成两倍速。
                    locked = min(old_count, new_count)
                    target = old_beats[::old_count // locked] - old_beats[0]
                    source = new_beats[::new_count // locked] - new_beats[0]
                    rates = np.diff(source) / np.diff(target)
                    if np.min(rates) < .82 or np.max(rates) > 1.18:
                        reasons["unstable_local_speed"] += 1
                        continue
                    voice_offset = float(np.interp(first_voice - new_beats[0], source, target))
                    # 起句在锁拍区后时用原速恢复的近似值筛选，最终以实际时间映射再次检查。
                    if first_voice - new_beats[0] > source[-1]:
                        voice_offset += (first_voice - new_beats[0] - source[-1]) / ratio
                    voice_at = start + voice_offset
                    gap = voice_at - last_voice
                    if not .25 <= gap <= 5:
                        reasons["unsafe_vocal_gap"] += 1
                        continue
                    progress = min(1, max(0, voice_offset / overlap))
                    mid_gain = math.cos(progress * math.pi / 2)
                    if end - voice_at > 1 or mid_gain > .25:
                        reasons["late_retirement"] += 1
                        continue
                    old_power = power(current, start, end) if power else .1
                    new_power = power(incoming, float(new_beats[0]), float(new_beats[-1])) if power else .1
                    if not math.isfinite(old_power + new_power) or min(old_power, new_power) < .0025:
                        reasons["quiet_transition"] += 1
                        continue
                    similarity = float(profile(current, start, end) @ profile(
                        incoming, float(new_beats[0]), float(new_beats[-1])
                    )) if profile else 1.0
                    if not math.isfinite(similarity) or similarity < .65:
                        reasons["incompatible_spectrum"] += 1
                        continue
                    boundary_cost = min(8, _boundary_distance(current, end)) * .08
                    boundary_cost += min(8, _boundary_distance(incoming, float(new_beats[0]))) * .015
                    # 权衡前奏保留与拖长混音；拍数没有固定优先级，长段落只在内容更匹配时胜出。
                    score = (
                        gap * .6 + abs(ratio - 1) * 8 + boundary_cost
                        + abs(math.log(old_power / new_power)) * .25
                        + old_tempo["jitter"] + new_tempo["jitter"]
                        + (1 - similarity) * 2 + float(new_beats[0]) * .045
                        + overlap * .02 + max(0, overlap - 8) ** 2 * .008
                        + max(0, end - voice_at) * .5 + mid_gain * .2
                    )
                    results.append({
                        "current_id": current["id"], "current_title": current["title"],
                        "next_id": incoming["id"], "next_title": incoming["title"],
                        "mix_start": start, "mix_end": end, "entry": float(new_beats[0]),
                        "overlap": overlap, "ratio": ratio, "current_last_vocal_end": last_voice,
                        "next_first_vocal": first_voice, "next_vocal_at_output": voice_at,
                        "estimated_vocal_gap": gap, "estimated_vocal_overlap": 0.0,
                        "actual_chroma_similarity": similarity, "score": score,
                        "current_tempo": old_tempo, "next_tempo": new_tempo, "bars": bars,
                        "outgoing_beats": old_count, "incoming_beats": new_count,
                        "locked_pulses": locked, "metrical_scale": scale,
                        "source_beat_offsets": source.tolist(), "target_beat_offsets": target.tolist(),
                        "retire_after_next_vocal_seconds": end - voice_at,
                        "outgoing_mid_gain_at_next_vocal": mid_gain,
                        "selection_reason": {
                            "vocal_gap_seconds": gap, "overlap_seconds": overlap,
                            "retained_intro_seconds": first_voice - float(new_beats[0]),
                            "skipped_intro_seconds": float(new_beats[0]),
                            "spectrum_similarity": similarity, "boundary_cost": boundary_cost,
                            "local_rate_min": float(np.min(rates)), "local_rate_max": float(np.max(rates)),
                        },
                    })
    results.sort(key=lambda candidate: candidate["score"])
    return {
        "status": "planned" if results else "rejected",
        "reason": "safe_phrase_candidates" if results else "no_safe_phrase_candidate",
        "candidates": results, "rejected_constraints": dict(reasons),
    }
