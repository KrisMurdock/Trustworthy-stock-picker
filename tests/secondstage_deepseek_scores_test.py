#!/usr/bin/env python3
"""Unit checks for the optional DeepSeek two-stage scorer."""

from __future__ import annotations

import argparse
import importlib.util
import io
import json
import os
import socket
import tempfile
import unittest
import urllib.error
from pathlib import Path
from unittest.mock import patch


ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "scripts" / "secondstage" / "deepseek_scores.py"
spec = importlib.util.spec_from_file_location("deepseek_scores", SCRIPT)
assert spec and spec.loader
deepseek_scores = importlib.util.module_from_spec(spec)
spec.loader.exec_module(deepseek_scores)


def feature_snapshot(offset: float) -> dict[str, float]:
    return {
        "ma20_distance": 2 + offset,
        "ma20_slope": 1 + offset,
        "return5": 3 + offset,
        "relative_return5": 4 + offset,
        "rsi14": 55 + offset,
        "close_position": 0.7,
        "volume_ratio": 1.2,
        "high20_distance": -5,
        "atr14_pct": 2.5,
        "upper_wick": 0.1,
    }


def response(body: dict) -> bytes:
    return json.dumps(body).encode("utf-8")


def score_response(scores: dict[str, float]) -> bytes:
    return response({
        "choices": [{
            "finish_reason": "stop",
            "message": {"content": json.dumps({"scores": [{"code": code, "score": value} for code, value in scores.items()]})},
        }],
        "usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
    })


class Transport:
    def __init__(self) -> None:
        self.calls = 0

    def __call__(self, request, timeout):
        self.calls += 1
        payload = json.loads(request.data)
        content = json.loads(payload["messages"][-1]["content"])
        codes = [row["code"] for row in content["candidates"]]
        return score_response({code: float(int(code[1:]) * 10) for code in codes})


class DeepSeekScoreTests(unittest.TestCase):
    def test_custom_config_fields_are_supported(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "llm.config"
            path.write_text(
                "bash_url: https://example.test/v1\n"
                "apikey: secret-value\n"
                "model: deepseek/flash\n"
                "effort: high\n",
                encoding="utf-8",
            )
            base, key, model, effort = deepseek_scores.load_config(path, "", "", "UNSET_DEEPSEEK_KEY")
            self.assertEqual(base, "https://example.test/v1")
            self.assertEqual(key, "secret-value")
            self.assertEqual(model, "deepseek/flash")
            self.assertEqual(effort, "high")
            _, _, _, overridden = deepseek_scores.load_config(path, "", "", "UNSET_DEEPSEEK_KEY", "none")
            self.assertEqual(overridden, "none")

    def test_missing_key_is_rejected_before_network(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "llm.config"
            path.write_text("bash_url: https://example.test/v1\nmodel: deepseek/flash\n", encoding="utf-8")
            with self.assertRaisesRegex(deepseek_scores.InputError, "API key"):
                deepseek_scores.load_config(path, "", "", "UNSET_DEEPSEEK_KEY")

    def test_invalid_http_endpoint_is_rejected(self) -> None:
        with patch.dict(os.environ, {"KEY": "secret"}, clear=False):
            with self.assertRaisesRegex(deepseek_scores.InputError, "loopback"):
                deepseek_scores.load_config(Path("/missing"), "http://example.test/v1", "m", "KEY")
            with self.assertRaisesRegex(deepseek_scores.InputError, "credentials"):
                deepseek_scores.load_config(Path("/missing"), "https://user:pass@example.test/v1", "m", "KEY")

    def test_score_parser_requires_exact_coverage(self) -> None:
        parsed = deepseek_scores.parse_score_content('{"scores":[{"code":"a","score":60},{"code":"b","score":40}]}', ["a", "b"])
        self.assertAlmostEqual(parsed["a"], 0.6)
        with self.assertRaisesRegex(deepseek_scores.InputError, "coverage"):
            deepseek_scores.parse_score_content('{"scores":[{"code":"a","score":60}]}', ["a", "b"])
        with self.assertRaisesRegex(deepseek_scores.InputError, "duplicate"):
            deepseek_scores.parse_score_content('{"scores":[{"code":"a","score":60},{"code":"a","score":40}]}', ["a"])

    def test_score_group_averages_three_orders(self) -> None:
        transport = Transport()
        client = deepseek_scores.Client("https://example.test/v1", "key", "model", transport=transport)
        candidates = [{"code": f"c{index}", "name": str(index)} for index in range(5)]
        scores, usages = client.score_group("stage2", "2026-10-21", candidates)
        self.assertEqual(transport.calls, 3)
        self.assertEqual(set(scores), {candidate["code"] for candidate in candidates})
        self.assertEqual(len(usages), 3)

    def test_retryable_http_error_is_retried(self) -> None:
        attempts = {"count": 0}
        delays: list[float] = []

        def transport(request, timeout):
            attempts["count"] += 1
            if attempts["count"] < 3:
                raise urllib.error.HTTPError("https://example.test", 503, "busy", {}, io.BytesIO(b""))
            return score_response({"a": 80, "b": 20})

        client = deepseek_scores.Client(
            "https://example.test/v1", "key", "model", transport=transport, sleeper=delays.append,
        )
        scores, _ = client.score_group("stage2", "2026-10-21", [{"code": "a"}, {"code": "b"}])
        self.assertEqual(attempts["count"], 4)
        self.assertEqual(delays, [1.0, 2.0])
        self.assertAlmostEqual(scores["a"], 0.8)

    def test_authentication_errors_are_not_retried(self) -> None:
        for status in (401, 403):
            with self.subTest(status=status):
                attempts = {"count": 0}
                delays: list[float] = []

                def transport(request, timeout):
                    attempts["count"] += 1
                    raise urllib.error.HTTPError("https://example.test", status, "denied", {}, io.BytesIO(b""))

                client = deepseek_scores.Client(
                    "https://example.test/v1", "key", "model", transport=transport, sleeper=delays.append,
                )
                with self.assertRaisesRegex(deepseek_scores.DeepSeekError, f"HTTP {status}"):
                    client._post("prompt")
                self.assertEqual(attempts["count"], 1)
                self.assertEqual(delays, [])

    def test_rate_limit_and_timeout_are_retried(self) -> None:
        for failure in (
            urllib.error.HTTPError("https://example.test", 429, "busy", {}, io.BytesIO(b"")),
            socket.timeout("slow"),
        ):
            with self.subTest(failure=type(failure).__name__):
                attempts = {"count": 0}
                delays: list[float] = []

                def transport(request, timeout, failure=failure):
                    attempts["count"] += 1
                    if attempts["count"] == 1:
                        raise failure
                    return score_response({"a": 80, "b": 20})

                client = deepseek_scores.Client(
                    "https://example.test/v1", "key", "model", transport=transport, sleeper=delays.append,
                )
                content, _ = client._post("prompt")
                self.assertIn('"scores"', content)
                self.assertEqual(attempts["count"], 2)
                self.assertEqual(delays, [1.0])

    def test_invalid_json_content_is_retried_without_reusing_scores(self) -> None:
        attempts = {"count": 0}

        def transport(request, timeout):
            attempts["count"] += 1
            payload = json.loads(request.data)
            content = json.loads(payload["messages"][-1]["content"])
            codes = [row["code"] for row in content["candidates"]]
            if attempts["count"] == 1:
                return response({
                    "choices": [{"finish_reason": "stop", "message": {"content": "not json"}}],
                })
            return score_response({code: 50.0 for code in codes})

        client = deepseek_scores.Client("https://example.test/v1", "key", "model", transport=transport, sleeper=lambda _: None)
        scores, usages = client.score_group("stage2", "2026-10-21", [{"code": "a"}, {"code": "b"}])
        self.assertEqual(attempts["count"], 3)
        self.assertEqual(len(usages), 2)
        self.assertEqual(scores, {"a": 0.5, "b": 0.5})

    def test_reasoning_effort_cli_argument_is_forwarded(self) -> None:
        args = deepseek_scores.parse_args([
            "--candidates", "c.json", "--dataset", "d.json", "--output-dir", "out", "--stage", "stage2",
            "--reasoning-effort", "low",
        ])
        self.assertEqual(args.reasoning_effort, "low")

    def test_output_token_limit_is_rejected(self) -> None:
        body = response({"choices": [{"finish_reason": "length", "message": {"content": "{}"}}]})
        with self.assertRaisesRegex(deepseek_scores.DeepSeekError, "output-token"):
            deepseek_scores.Client._extract(json.loads(body), cached=False)

    def test_invalid_envelope_is_not_cached(self) -> None:
        attempts = {"count": 0}
        with tempfile.TemporaryDirectory() as temporary:
            cache = Path(temporary)

            def transport(request, timeout):
                attempts["count"] += 1
                if attempts["count"] == 1:
                    return response({"choices": []})
                return score_response({"a": 80, "b": 20})

            client = deepseek_scores.Client(
                "https://example.test/v1", "key", "model", transport=transport, cache_dir=cache, sleeper=lambda _: None,
            )
            with self.assertRaisesRegex(deepseek_scores.DeepSeekError, "no completion choices"):
                client._post("prompt")
            self.assertEqual(list(cache.iterdir()), [])
            content, _ = client._post("prompt")
            self.assertIn('"scores"', content)
            self.assertEqual(attempts["count"], 2)

    def test_stage1_execute_writes_artifact_and_next_stage_input(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            candidates = []
            samples = []
            for index in range(6):
                code = f"c{index:02d}"
                features = feature_snapshot(index)
                candidates.append({
                    "code": code,
                    "name": code,
                    "rank": index + 1,
                    "base_score": 50 + index,
                    "blend_score": 0.5 + index / 100,
                    "features": features,
                })
                samples.append({"as_of": "2026-10-21", "code": code, "features": features})
            candidates_path = root / "candidates.json"
            dataset_path = root / "dataset.json"
            candidates_path.write_text(json.dumps({"schema_version": 1, "dates": {"2026-10-21": candidates}}), encoding="utf-8")
            dataset_path.write_text(json.dumps({"samples": samples}), encoding="utf-8")
            args = argparse.Namespace(
                candidates=candidates_path,
                dataset=dataset_path,
                output_dir=root / "scores",
                stage="stage1",
                dates="2026-10-21",
                max_dates=0,
                group_size=5,
                workers=2,
                config=Path("/unused"),
                base_url="",
                model="",
                api_key_env="UNUSED",
                timeout=10,
                retries=1,
                max_output_tokens=256,
                cache_dir=root / "cache",
                no_cache=False,
                skip_existing=False,
                top_out=root / "stage2.json",
                top_n=3,
            )
            client = deepseek_scores.Client("https://example.test/v1", "key", "model", transport=Transport())
            self.assertEqual(deepseek_scores.execute(args, client=client), 0)
            artifact = json.loads((root / "scores" / "2026-10-21.json").read_text(encoding="utf-8"))
            self.assertEqual(artifact["schema_version"], 1)
            self.assertEqual(len(artifact["scores"]), 6)
            self.assertEqual(artifact["model"]["provider"], "deepseek")
            next_document = json.loads((root / "stage2.json").read_text(encoding="utf-8"))
            top = next_document["dates"]["2026-10-21"]
            self.assertEqual([row["code"] for row in top], ["c05", "c04", "c03"])


if __name__ == "__main__":
    unittest.main()
