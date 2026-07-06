"""新闻聚合控制器 — 从 app.py 提取 _news_loop + 板块增强 + 推送."""

from __future__ import annotations

import asyncio
import datetime as dt
import logging
from collections import Counter
from pathlib import Path
from typing import Callable

from stock_watcher.config import NewsConfig, DeepSeekConfig
from stock_watcher.models import NewsItem, SectorStock, TrendScore
from stock_watcher.news import (
    EastMoneyStockNewsFetcher,
    EastMoneyFlashNewsFetcher,
    SinaFlashNewsFetcher,
    CLSTelegraphFetcher,
    YahooNewsFetcher,
    NewsAggregator,
    NewsClassifier,
    NewsLevel,
    build_enriched_card,
)
from stock_watcher.news.sector_matcher import SectorMatcher
from stock_watcher.news.trend_scorer import TrendScorer
from stock_watcher.news.trend_ranker import TrendRanker

logger = logging.getLogger(__name__)


class NewsController:
    """新闻聚合 + AI 分类 + 板块增强 + 飞书推送."""

    def __init__(
        self,
        news_cfg: NewsConfig,
        deepseek_cfg: DeepSeekConfig | None = None,
        get_watchlist: Callable[[], list[str]] | None = None,
        on_push: Callable[[dict], None] | None = None,
        on_stats: Callable[[str], None] | None = None,
    ) -> None:
        self._cfg = news_cfg
        self._deepseek_cfg = deepseek_cfg
        self._get_watchlist = get_watchlist or (lambda: [])
        self._on_push = on_push
        self._on_stats = on_stats
        self._aggregator: NewsAggregator | None = None
        self._classifier: NewsClassifier | None = None
        self._stats: dict[str, int] = {"fetched": 0, "filtered": 0, "critical": 0}

    async def run(self, stop_event: asyncio.Event) -> None:
        if not self._cfg or not self._cfg.enabled_sources:
            return

        cfg = self._cfg
        fetchers = self._build_fetchers(cfg)
        if not fetchers:
            logger.warning("NewsController: no enabled fetchers")
            return

        seen_path = Path(cfg.seen_ids_path) if cfg.seen_ids_path else Path("news_seen.jsonl")
        self._aggregator = NewsAggregator(fetchers, str(seen_path))
        self._classifier = NewsClassifier(
            deepseek_cfg=self._deepseek_cfg,
            critical_keywords=cfg.critical_keywords,
            ignore_keywords=cfg.ignore_keywords,
        )
        sector_matcher = SectorMatcher(
            sector_keywords=cfg.sector_keywords.keywords,
            sector_list=None,
        )
        trend_scorer = TrendScorer(
            ma_weight=cfg.trend_scoring.ma_weight,
            macd_weight=cfg.trend_scoring.macd_weight,
            rsi_weight=cfg.trend_scoring.rsi_weight,
            consecutive_up_weight=cfg.trend_scoring.consecutive_up_weight,
            volume_ratio_weight=cfg.trend_scoring.volume_ratio_weight,
        )
        trend_ranker = TrendRanker(
            deepseek_cfg=self._deepseek_cfg,
            min_score=cfg.trend_scoring.min_score_threshold,
            top_n_candidates=cfg.trend_scoring.top_n_candidates,
            top_n_final=cfg.trend_scoring.top_n_final,
            max_stocks=cfg.trend_scoring.max_stocks_per_card,
        )

        logger.info("NewsController started (%d fetchers, interval %ds)", len(fetchers), cfg.poll_interval)

        while not stop_event.is_set():
            try:
                codes = self._get_watchlist()
                items = await self._aggregator.aggregate(codes)
                self._stats["fetched"] = len(items)

                if not items:
                    await asyncio.sleep(cfg.poll_interval)
                    continue

                src_counts = Counter(i.source for i in items)
                logger.info("news: %d items %s", len(items),
                            " ".join(f"{s}={c}" for s, c in src_counts.items()))

                # Classify
                classified = []
                critical_n = 0
                for item in items:
                    result = await self._classifier.classify(item)
                    classified.append((item, result))
                    if result.level == NewsLevel.CRITICAL:
                        critical_n += 1

                self._stats["filtered"] = len(items) - critical_n
                self._stats["critical"] = critical_n

                if self._on_stats:
                    self._on_stats(f"新闻 {len(items)}条 🔴{critical_n}")

                # Push critical news with sector enrichment
                for item, result in classified:
                    if result.level != NewsLevel.CRITICAL:
                        continue
                    sectors = sector_matcher.match(item)
                    tiered = None
                    if sectors:
                        try:
                            import akshare
                            all_stocks: list[SectorStock] = []
                            for sec_name in sectors[:3]:
                                try:
                                    df = await asyncio.to_thread(
                                        akshare.stock_board_concept_cons_em, symbol=sec_name
                                    )
                                    for _, row in df.iterrows():
                                        all_stocks.append(SectorStock(
                                            code=str(row.get("代码", "")),
                                            name=str(row.get("名称", "")),
                                            sector=sec_name,
                                            price=float(row.get("最新价", 0) or 0),
                                            change_pct=float(row.get("涨跌幅", 0) or 0),
                                            high=float(row.get("最高", 0) or 0),
                                            low=float(row.get("最低", 0) or 0),
                                            open=float(row.get("今开", 0) or 0),
                                            prev_close=float(row.get("昨收", 0) or 0),
                                            volume=int(row.get("成交量", 0) or 0),
                                            turnover=float(row.get("换手率", 0) or 0),
                                            pe=float(row.get("市盈率-动态", 0) or 0),
                                            pb=float(row.get("市净率", 0) or 0),
                                        ))
                                except Exception:
                                    logger.warning("Sector fetch failed for %s", sec_name, exc_info=True)

                            if all_stocks:
                                stock_scores: dict[str, TrendScore] = {}
                                sem = asyncio.Semaphore(5)

                                async def fetch_hist(s: SectorStock):
                                    async with sem:
                                        return await asyncio.to_thread(
                                            akshare.stock_zh_a_hist_tx,
                                            symbol=f"sh{s.code}" if s.code.startswith("6") else f"sz{s.code}",
                                            start_date=(dt.datetime.now() - dt.timedelta(days=90)).strftime("%Y%m%d"),
                                            end_date=dt.datetime.now().strftime("%Y%m%d"),
                                        )

                                tasks_list = [fetch_hist(s) for s in all_stocks[:30]]
                                results = await asyncio.gather(*tasks_list, return_exceptions=True)
                                for s, df_or_exc in zip(all_stocks[:30], results):
                                    if isinstance(df_or_exc, Exception):
                                        stock_scores[s.code] = TrendScore(code=s.code)
                                    else:
                                        stock_scores[s.code] = trend_scorer.score(s.code, df_or_exc)

                                watchlist = set(self._get_watchlist())
                                tiered = trend_ranker.rank(all_stocks, stock_scores, watchlist, watchlist)
                        except Exception:
                            logger.warning("Sector enrichment failed", exc_info=True)

                    card = build_enriched_card(
                        news=item, classify=result,
                        sector=sectors[0] if sectors else None,
                        tiered=tiered,
                    )
                    if self._on_push:
                        self._on_push(card)

                self._aggregator.persist_seen()
            except Exception:
                logger.warning("NewsController cycle error", exc_info=True)

            await asyncio.sleep(cfg.poll_interval)

    def _build_fetchers(self, cfg: NewsConfig):
        fetchers = []
        sources = cfg.sources
        if sources.get("eastmoney_stock", True):
            fetchers.append(EastMoneyStockNewsFetcher(enabled=True))
        if sources.get("eastmoney_flash", True):
            fetchers.append(EastMoneyFlashNewsFetcher(enabled=True))
        if sources.get("sina_flash", True):
            fetchers.append(SinaFlashNewsFetcher(enabled=True))
        if sources.get("cls_telegraph", True):
            fetchers.append(CLSTelegraphFetcher(enabled=True))
        if sources.get("yahoo_news", True):
            fetchers.append(YahooNewsFetcher(enabled=True))
        return fetchers

    @property
    def stats(self) -> dict[str, int]:
        return dict(self._stats)
