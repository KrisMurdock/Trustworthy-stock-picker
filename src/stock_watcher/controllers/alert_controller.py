"""告警规则管理控制器 — CRUD + 持久化."""

from __future__ import annotations

import logging
from pathlib import Path
from typing import TYPE_CHECKING

from stock_watcher.config import save_alerts
from stock_watcher.models import AlertRule, AlertType

if TYPE_CHECKING:
    pass

logger = logging.getLogger(__name__)


class AlertController:
    """Manages alert rules: add, remove, check, persist."""

    def __init__(self, alerts: list[AlertRule] | None = None) -> None:
        self.rules: list[AlertRule] = list(alerts) if alerts else []
        self._history: list[str] = []

    def add(self, code: str, alert_type: str, value: float) -> AlertRule:
        rule = AlertRule(code=code, alert_type=AlertType(alert_type), value=value)
        self.rules.append(rule)
        return rule

    def remove(self, code: str, alert_type: str) -> bool:
        target = AlertType(alert_type)
        for i, r in enumerate(self.rules):
            if r.code == code and r.alert_type == target:
                self.rules.pop(i)
                return True
        return False

    def update_codes(self, watchlist: list[str]) -> None:
        """Remove alerts for stocks no longer in watchlist."""
        wl_set = set(watchlist)
        self.rules = [r for r in self.rules if r.code in wl_set]

    def save(self, config_path: Path) -> None:
        save_alerts(config_path, self.rules)

    def log_history(self, alert: AlertRule, quote_text: str) -> None:
        self._history.append(quote_text)

    def get_recent_history(self, count: int = 200) -> list[str]:
        return self._history[-count:]
