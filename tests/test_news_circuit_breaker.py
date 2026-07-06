"""Tests for 3-state circuit breaker in BaseNewsFetcher."""
import asyncio
import time
import pytest
from stock_watcher.news.base import BaseNewsFetcher


class _TestFetcher(BaseNewsFetcher):
    source_name = "test_cb"
    async def fetch(self, watchlist_codes: list[str]) -> list:
        return []


class TestCircuitBreakerStates:
    """Test GREEN → RED → YELLOW → GREEN transitions."""

    def test_initial_state_is_green(self):
        f = _TestFetcher()
        assert f.state == "green"
        assert not f.is_failing
        assert not f.should_probe()

    def test_five_failures_trigger_red(self):
        f = _TestFetcher()
        for _ in range(5):
            f._record_failure()
        assert f.state == "red"
        assert f.is_failing
        assert not f.should_probe()  # just entered red, still cooling

    def test_success_resets_to_green(self):
        f = _TestFetcher()
        for _ in range(3):
            f._record_failure()
        f._record_success()
        assert f.state == "green"
        assert f._failure_history == []

    def test_red_transitions_to_yellow_after_cooldown(self, monkeypatch):
        f = _TestFetcher()
        # Override cooldown to 0.1s for test
        f._cooldown_period = 0.1
        f._probe_interval = 0.05

        # Trigger RED
        for _ in range(5):
            f._record_failure()
        assert f.state == "red"

        # Wait for cooldown
        time.sleep(0.15)
        assert f.state == "yellow"
        assert not f.is_failing  # YELLOW is not "failing"
        assert f.should_probe()   # ready to probe

    def test_yellow_probe_success_returns_to_green(self, monkeypatch):
        f = _TestFetcher()
        f._cooldown_period = 0.1
        f._probe_interval = 0.05

        for _ in range(5):
            f._record_failure()
        time.sleep(0.15)
        assert f.state == "yellow"

        f._record_success()
        assert f.state == "green"

    def test_yellow_probe_failure_stays_yellow(self, monkeypatch):
        f = _TestFetcher()
        f._cooldown_period = 0.1
        f._probe_interval = 0.05

        for _ in range(5):
            f._record_failure()
        time.sleep(0.15)
        assert f.state == "yellow"

        f._record_failure()
        assert f.state == "yellow"  # stays yellow
        assert not f.should_probe()  # must wait probe_interval

        time.sleep(0.1)
        assert f.should_probe()  # ready again

    def test_old_failures_decay_from_window(self, monkeypatch):
        f = _TestFetcher()
        f._failure_window = 0.1  # very short window

        for _ in range(5):
            f._record_failure()
        assert f.state == "red"

        # Wait for failures to decay out of window
        time.sleep(0.15)
        # Failures older than window are pruned, count drops below threshold
        assert f.state in ("yellow", "green")

    def test_is_failing_matches_red_only(self, monkeypatch):
        f = _TestFetcher()
        f._cooldown_period = 0.1
        f._probe_interval = 0.05

        assert not f.is_failing  # GREEN

        for _ in range(5):
            f._record_failure()
        assert f.is_failing  # RED

        time.sleep(0.15)
        assert not f.is_failing  # YELLOW

    def test_cooldown_remaining_decreases(self, monkeypatch):
        f = _TestFetcher()
        f._cooldown_period = 10.0

        for _ in range(5):
            f._record_failure()

        r1 = f.cooldown_remaining
        assert r1 > 0
        time.sleep(0.2)
        r2 = f.cooldown_remaining
        assert r2 < r1
