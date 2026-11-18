"""自动选择乐句、按计划渲染并在发布前验收；失败时尝试下一份安全计划。"""

import argparse
import copy
import hashlib
import json
from pathlib import Path
import traceback
from concurrent.futures import ThreadPoolExecutor

import numpy as np
import soundfile as sf

from engine_runtime import ROOT, RATE, engine, renderer


def write_report(path, report):
    path.write_text(json.dumps(report, ensure_ascii=False, indent=2), encoding="utf-8")


def publish_candidate_audio(directory, phrase, crossfade, report):
    if not report["checks"] or not all(report["checks"].values()):
        raise ValueError("质量检查失败，禁止覆盖上一次音频")
    expected = round(report["duration_seconds"] * RATE)
    for audio in (phrase, crossfade):
        if audio.shape != (expected, 2) or not np.isfinite(audio).all():
            raise ValueError("发布音频必须是有限值的帧数乘双声道数组")
    # 原子写入接口接收帧在前的数组；只有完整音频写入后才发布 accepted 报告。
    engine.atomic_write_audio(directory / "phrase.wav", phrase, "FLOAT")
    engine.atomic_write_audio(directory / "crossfade.wav", crossfade, "FLOAT")
    write_report(directory / "report.json", report)


def render_candidate(candidate, current, incoming, directory, gain_ratio=1.0):
    directory.mkdir(parents=True, exist_ok=True)
    locked = candidate["locked_pulses"]
    source_offsets = np.asarray(candidate["source_beat_offsets"])
    offsets = np.asarray(candidate["target_beat_offsets"])
    overlap = candidate["overlap"]
    lead, tail = 6.0, 10.0
    seconds = lead + overlap + tail
    frames = round(seconds * RATE)
    old_start = candidate["mix_start"] - lead
    if old_start < 0:
        raise ValueError("曲目太短，无法保留过渡前上下文")
    old = renderer.segment(current["input"], old_start, frames)
    old_stems, old_gains = renderer.stems_for(current, old_start, frames)
    # 设备在交接后使用新曲增益；预混旧曲只补相对差，避免响度归一化被重复应用。
    old *= gain_ratio
    old_stems *= gain_ratio
    source_frames = round((seconds + 8) * max(candidate["ratio"], 1) * RATE)
    new_stems, new_gains = renderer.stems_for(incoming, candidate["entry"], source_frames)
    native_calibration = {}
    first, last = round(lead * RATE), round((lead + overlap) * RATE)
    for role, drums, grid in (
        ("outgoing", old_stems[0, first:last], offsets),
        ("incoming", new_stems[0, :round(source_offsets[-1]*RATE)], source_offsets),
    ):
        native_calibration[role] = {
            str(subdivision): engine.drum_grid_qa(drums.T, grid, subdivision)
            for subdivision in (1, 2, 4)
        }
        if not any(item["passed"] for item in native_calibration[role].values()):
            return {"status": "rejected", "checks": {f"{role}_native_rhythm": False}}
    reference = directory / "incoming-reference.wav"
    sf.write(reference, new_stems[0], RATE, subtype="FLOAT")
    source_period = float(source_offsets[-1]) / locked
    target_period = overlap / locked
    release = min(6.48, max(2.0, target_period * (8 if abs(candidate["ratio"] - 1) > .03 else 4)))
    timing = {
        "transition_pulses": locked, "incoming_cue_pulse": locked // 2,
        "target_offsets_seconds": offsets.tolist(), "target_pulse_interval_seconds": target_period,
        "pulse_bpm": 60 / target_period, "tempo_release_seconds": release,
    }
    continuation = source_offsets[-1] + np.arange(1, 100) * source_period
    refinement = {
        "incoming_pulse_bpm": 60 / source_period, "incoming_pulse_interval_seconds": source_period,
        "incoming_beat_times_seconds": np.r_[source_offsets, continuation].tolist(),
        "refined_cue_seconds": float(source_offsets[locked // 2]),
    }
    time_map = engine.build_tempo_map({}, reference, timing, refinement, map_path=directory / "tempo.map")
    jobs = []
    for name, samples in zip(renderer.STEM_NAMES, new_stems):
        source, destination = directory / f"incoming-{name}-source.wav", directory / f"incoming-{name}-locked.wav"
        sf.write(source, samples, RATE, subtype="FLOAT")
        jobs.append((source, destination))
    with ThreadPoolExecutor(max_workers=2) as pool:
        futures = [pool.submit(engine.rubberband_audio, source, destination,
                               time_map["path"], time_map["output_duration"]) for source, destination in jobs]
        for future in futures:
            future.result()
    rendered = []
    for _, destination in jobs:
        audio, rate = sf.read(destination, dtype="float32", always_2d=True)
        assert rate == RATE
        rendered.append(audio)
    rendered = np.stack(rendered)
    voice_offset = float(np.interp(
        candidate["next_first_vocal"] - candidate["entry"],
        time_map["source_seconds"], time_map["target_seconds"],
    ))
    voice_start = lead + voice_offset
    voice_end = candidate["current_last_vocal_end"] - old_start
    gap = voice_start - voice_end
    rendered_candidate = copy.deepcopy(candidate)
    rendered_candidate["next_first_vocal"] = candidate["entry"] + voice_offset * candidate["ratio"]
    phrase, vocal_report = renderer.make_phrase_mix(
        old_stems, rendered, old, rendered_candidate, lead, frames
    )
    placed = renderer.padded(rendered.sum(axis=0), frames, round(lead * RATE))
    crossfade = renderer.equal_power(old, placed, np.arange(frames) / RATE, lead, lead + overlap)
    first, last = round(lead * RATE), round((lead + overlap) * RATE)
    drum_reports = {}
    for role, native_drums, native_grid, output_drums in (
        ("outgoing", old_stems[0, first:last], offsets, old_stems[0, first:last]),
        ("incoming", new_stems[0, :round(source_offsets[-1] * RATE)], source_offsets,
         rendered[0, :last-first]),
    ):
        calibration = native_calibration[role]
        eligible = [item for item in calibration.values() if item["passed"]]
        subdivision = min(
            eligible, key=lambda item: item["p90_absolute_phase_ms"]
            / (float(np.median(np.diff(native_grid))) / item["drum_subdivision"]),
        )["drum_subdivision"] if eligible else 1
        actual = engine.drum_grid_qa(output_drums.T, offsets, subdivision)
        shift = round(float(np.median(np.diff(offsets))) / subdivision / 2 * RATE)
        shifted = np.pad(output_drums, ((shift, 0), (0, 0)))[:len(output_drums)]
        negative = engine.drum_grid_qa(shifted.T, offsets, subdivision)
        drum_reports[role] = {
            "calibration": calibration, "selected_subdivision": subdivision,
            "actual": actual, "wrong_phase_control": negative,
            "passed": bool(eligible) and actual["passed"] and not negative["passed"],
        }
    relative_phase = (
        drum_reports["incoming"]["actual"].get("median_signed_phase_ms", float("nan"))
        - drum_reports["outgoing"]["actual"].get("median_signed_phase_ms", float("nan"))
    )
    window = round(.05 * RATE)
    handoff = phrase[first:last]
    blocks = handoff[:len(handoff) // window * window].reshape(-1, window, 2)
    rms = np.sqrt(np.mean(blocks ** 2, axis=(1, 2)))
    checks = {
        "finite_audio": bool(np.isfinite(phrase).all()),
        "no_50ms_silent_windows": bool(np.min(rms) > 1e-5),
        "old_voice_gain_at_last_vocal": vocal_report["old_voice_gain_at_last_vocal"] >= .999,
        "new_voice_gain_at_first_vocal": vocal_report["new_voice_gain_at_first_vocal"] >= .999,
        "vocal_handoff": .25 <= gap <= 5,
        "old_music_retires_at_new_voice": lead + overlap - voice_start <= 1,
        "old_mid_gain_at_new_voice": bool(
            np.cos(np.clip(voice_offset / overlap, 0, 1) * np.pi / 2) <= .25
        ),
        "outgoing_beat_grid": drum_reports["outgoing"]["passed"],
        "incoming_beat_grid": drum_reports["incoming"]["passed"],
        "relative_beat_phase": bool(abs(relative_phase) <= .12 * target_period * 1000),
        "same_source_prefix": bool(np.max(abs(phrase[:first] - old[:first])) < 1e-6),
    }
    report = {
        "algorithm": "adaptive phrase engine; local content-ranked duration + per-beat warp + stem envelopes",
        "candidate": candidate, "status": "accepted" if all(checks.values()) else "rejected",
        "checks": checks, "lead_seconds": lead, "duration_seconds": seconds,
        "vocal_start_seconds": voice_start, "vocal_end_seconds": voice_end, "vocal_gap_seconds": gap,
        "drum_alignment": drum_reports, "relative_phase_ms": relative_phase,
        "minimum_50ms_rms_dbfs": float(20 * np.log10(max(float(np.min(rms)), 1e-12))),
        "vocal_preservation": vocal_report, "tempo_release_seconds": release,
        "source_stem_gains": {"old": old_gains, "new": new_gains},
        "source_map_seconds": time_map["source_seconds"].tolist(),
        "target_map_seconds": time_map["target_seconds"].tolist(),
        "subjective_verdict": "not played to sound device; user audition pending",
    }
    if all(checks.values()):
        publish_candidate_audio(directory, phrase, crossfade, report)
    else:
        write_report(directory / "report.json", report)
    return report


def render_pair(current_id, next_id, output):
    library = {track["id"]: track for track in json.loads((ROOT / "library.json").read_text(encoding="utf-8"))}
    from adaptive_planner import plan_pair
    from plan_library import power, profile
    pair = plan_pair(library[current_id], library[next_id], profile=profile, power=power)
    summary = json.loads((ROOT / "planning-summary.json").read_text(encoding="utf-8"))
    assert hashlib.sha256((ROOT / "adaptive_planner.py").read_bytes()).hexdigest() == summary["planner_sha256"]
    assert hashlib.sha256((ROOT / "library.json").read_bytes()).hexdigest() == summary["features_sha256"]
    output.mkdir(parents=True, exist_ok=True)
    write_report(output / "plan.json", pair)
    preflight = json.loads((ROOT / "rhythm-preflight.json").read_text(encoding="utf-8"))
    for path, fingerprint in preflight["sources"].items():
        assert hashlib.sha256(Path(path).read_bytes()).hexdigest() == fingerprint, "原曲节奏预检过期"
    native_pair = next((row for row in preflight["results"]
                        if row["current_id"] == current_id and row["next_id"] == next_id), None)
    attempts = []
    # 每个曲对按安全计划排名重试，失败结果留存，只有通过的音频能成为最终产物。
    for index, candidate in enumerate(pair["candidates"][:20], 1):
        directory = output / f"attempt-{index:02d}"
        if native_pair is not None and index <= len(native_pair["candidates"]):
            evidence = native_pair["candidates"][index-1]
            assert evidence["outgoing_beats"] == candidate["outgoing_beats"]
            assert abs(evidence["seconds"] - candidate["overlap"]) < 1e-6
            if not evidence["passed"]:
                directory.mkdir(exist_ok=True)
                write_report(directory / "native-rejection.json", evidence)
                attempts.append({
                    "directory": str(directory), "status": "rejected", "stage": "native_rhythm",
                    "outgoing_beats": candidate["outgoing_beats"],
                    "incoming_beats": candidate["incoming_beats"], "overlap": candidate["overlap"],
                    "failed_checks": ["insufficient_native_rhythm_evidence"],
                })
                continue
        print(json.dumps({
            "stage": "render_attempt", "pair": output.name, "attempt": index,
            "beats": candidate["outgoing_beats"], "incoming_beats": candidate["incoming_beats"],
            "seconds": candidate["overlap"],
        }), flush=True)
        try:
            report = render_candidate(candidate, library[current_id], library[next_id], directory)
        except Exception as error:
            report = {"status": "rejected", "error": str(error), "traceback": traceback.format_exc()}
            directory.mkdir(exist_ok=True)
            write_report(directory / "error.json", report)
        attempts.append({
            "directory": str(directory), "status": report["status"],
            "outgoing_beats": candidate["outgoing_beats"], "incoming_beats": candidate["incoming_beats"],
            "overlap": candidate["overlap"],
            "failed_checks": [key for key, passed in report.get("checks", {}).items() if not passed],
            "error": report.get("error"),
        })
        if report["status"] == "accepted":
            break
    result = {
        "current_id": current_id, "next_id": next_id,
        "current_title": library[current_id]["title"], "next_title": library[next_id]["title"],
        "status": "accepted" if attempts and attempts[-1]["status"] == "accepted" else "rejected",
        "attempts": attempts, "planning_status": pair["status"],
        "sources": {str(path): hashlib.sha256(path.read_bytes()).hexdigest()
                    for path in (Path(__file__), ROOT / "adaptive_planner.py",
                                 ROOT / "engine-adaptive/stem_transition.py", ROOT / "pair-plans.json",
                                 ROOT / "library.json")},
        "audio_policy": "all raw audio and features stay local; socket/DNS denied; no sound device",
    }
    write_report(output / "result.json", result)
    print(json.dumps({"stage": "ADAPTIVE_PAIR_DONE", **result}, ensure_ascii=True), flush=True)
    return result


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("current_id")
    parser.add_argument("next_id")
    parser.add_argument("--name", required=True)
    arguments = parser.parse_args()
    assert arguments.name.replace("-", "").isalnum()
    result = render_pair(arguments.current_id, arguments.next_id, ROOT / "renders" / arguments.name)
    raise SystemExit(0 if result["status"] == "accepted" else 1)
