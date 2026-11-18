"""从公开选点接口验证拍数适应、人声保护和拍速层级。"""

import copy
import json
import unittest
import numpy as np

from adaptive_planner import plan_pair


def track(identifier, duration=120, period=.5, first_voice=4, last_voice=114):
    count = int(duration / period)
    return {
        "id": identifier, "title": identifier, "duration": duration,
        "beats": [index * period for index in range(count + 1)],
        "downbeats": [index * 4 * period for index in range(count // 4 + 1)],
        "vocal_regions_estimated": [{"start": first_voice, "end": last_voice}],
        "segments": [{"start": 0, "end": duration, "label": "section_1"}],
    }


class AdaptivePlannerTests(unittest.TestCase):
    # 首次本机分析带有 NumPy 标量；质量报告必须与从 JSON 缓存读取时一样可序列化。
    def test_fresh_model_scalars_produce_serializable_handoff_checks(self):
        current = track("old", last_voice=np.float64(115))
        incoming = track("new", first_voice=np.float64(4))
        plan = plan_pair(current, incoming)["candidates"][0]
        json.dumps({"vocal_handoff": .25 <= plan["estimated_vocal_gap"] <= 5})

    # 短前奏只能承接短乐句，不能沿用固定三十二拍导致无入口。
    def test_short_intro_uses_eight_beats_and_preserves_first_line(self):
        current = track("outgoing", last_voice=115)
        incoming = track("incoming", first_voice=4, last_voice=110)
        result = plan_pair(current, incoming)
        self.assertEqual(result["status"], "planned")
        best = result["candidates"][0]
        self.assertEqual(best["outgoing_beats"], 8)
        self.assertLessEqual(best["entry"], 3.65)
        self.assertGreaterEqual(best["estimated_vocal_gap"], .25)
        self.assertLessEqual(best["estimated_vocal_gap"], 5)

    # 足够长的前奏应产生多种拍数候选，不能把三十二拍简单换成另一个固定值。
    def test_long_intro_keeps_different_safe_lengths_available(self):
        result = plan_pair(track("old", last_voice=115), track("new", first_voice=20))
        counts = {candidate["outgoing_beats"] for candidate in result["candidates"]}
        self.assertEqual(counts, {8, 16, 24, 32})

    # 同样八拍在一百五十拍速时长是三点二秒，不能套用一百二十拍速的四秒。
    def test_duration_comes_from_the_actual_beat_span(self):
        result = plan_pair(track("old", period=.4, last_voice=116.2),
                           track("new", period=.4, first_voice=3.2))
        self.assertEqual(result["candidates"][0]["outgoing_beats"], 8)
        self.assertAlmostEqual(result["candidates"][0]["overlap"], 3.2)

    # 半拍速层级只影响网格子序列，真实音频仍接近原速。
    def test_half_time_family_does_not_double_the_audio_speed(self):
        result = plan_pair(track("old", period=1, last_voice=115),
                           track("new", period=.5, first_voice=8))
        self.assertEqual(result["status"], "planned")
        best = result["candidates"][0]
        self.assertEqual(best["metrical_scale"], 2)
        self.assertAlmostEqual(best["ratio"], 1)
        self.assertEqual(len(best["source_beat_offsets"]), len(best["target_beat_offsets"]))

    # 下首开头就在唱时，不跳过正文来换取看似安全的候选。
    def test_no_intro_cannot_be_replaced_by_skipping_the_first_line(self):
        result = plan_pair(track("old"), track("new", first_voice=0))
        self.assertEqual(result["status"], "rejected")
        self.assertEqual(result["candidates"], [])

    # 特征不是可信控制指令，非有限时间必须在规划入口就拒绝。
    def test_nonfinite_beat_features_are_rejected(self):
        current = track("old")
        current["beats"][8] = float("nan")
        self.assertEqual(plan_pair(current, track("new"))["reason"], "invalid_features")

    # 对同一曲库反复规划不能改写上一次分析的人声或拍点。
    def test_planning_keeps_input_features_unchanged(self):
        current, incoming = track("old"), track("new", first_voice=20)
        original = copy.deepcopy((current, incoming))
        plan_pair(current, incoming)
        self.assertEqual((current, incoming), original)

    # 弦乐等分轨残留可能只冒出一小段伪人声，不能据此规划“首句接唱”。
    def test_isolated_tiny_vocal_residue_is_not_a_reliable_song_line(self):
        result = plan_pair(track("old"), track("new", first_voice=11.75, last_voice=13))
        self.assertEqual(result["status"], "rejected")
        self.assertEqual(result["reason"], "no_reliable_vocal_handoff")


if __name__ == "__main__":
    unittest.main()
