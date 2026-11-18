"""成品调用契约只允许本机 PCM，发布仍由实际音频门禁控制。"""

import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import numpy as np
import soundfile as sf

from render import publish_candidate_audio
from engine_runtime import engine


class RenderContractTests(unittest.TestCase):
    # 失败结果不得破坏最后可用文件，避免回退依赖一份已被覆盖的坏音频。
    def test_rejected_output_does_not_replace_previous_audio(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            target = directory / "phrase.wav"
            target.write_bytes(b"accepted")
            with self.assertRaises(ValueError):
                publish_candidate_audio(directory, np.zeros((4410, 2)), np.zeros((4410, 2)),
                                        {"checks": {"beat_grid": False}, "duration_seconds": .1})
            self.assertEqual(target.read_bytes(), b"accepted")

    # 真正落盘并解码，防止又把帧轴和声道轴传反。
    def test_accepted_output_is_a_decodable_stereo_wave(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            samples = np.full((4410, 2), .1, dtype=np.float32)
            publish_candidate_audio(directory, samples, samples,
                                    {"checks": {"beat_grid": True}, "duration_seconds": .1})
            decoded, rate = sf.read(directory / "phrase.wav", always_2d=True)
            self.assertEqual(rate, 44100)
            self.assertEqual(decoded.shape, (4410, 2))

    # 八拍计划的每个边界和恢复原速必须由本次参数决定，不能依赖旧三十二拍全局值。
    def test_short_phrase_map_remains_monotonic_and_recovers_native_speed(self):
        with tempfile.TemporaryDirectory() as temporary, patch.object(engine, "SR", 1000):
            directory = Path(temporary)
            source = directory / "source.wav"
            sf.write(source, np.zeros((50000, 2)), 1000, subtype="FLOAT")
            timing = {
                "transition_pulses": 8, "incoming_cue_pulse": 4,
                "target_offsets_seconds": [index*.5 for index in range(9)],
                "target_pulse_interval_seconds": .5, "pulse_bpm": 120,
                "tempo_release_seconds": 2,
            }
            incoming = {
                "incoming_pulse_bpm": 125, "incoming_pulse_interval_seconds": .48,
                "incoming_beat_times_seconds": [index*.48 for index in range(100)],
                "refined_cue_seconds": 1.92,
            }
            mapping = engine.build_tempo_map({}, source, timing, incoming, map_path=directory / "tempo.map")
            self.assertAlmostEqual(mapping["transition_end_source"], 3.84)
            self.assertAlmostEqual(mapping["transition_end_target"], 4)
            self.assertAlmostEqual(mapping["release_target_end"], 6)
            self.assertTrue(np.all(np.diff(mapping["source_seconds"]) > 0))
            self.assertTrue(np.all(np.diff(mapping["target_seconds"]) > 0))
            self.assertEqual(mapping["release_points"][-1]["playback_rate"], 1)


if __name__ == "__main__":
    unittest.main()
