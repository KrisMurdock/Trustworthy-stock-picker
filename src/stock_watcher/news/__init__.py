"""新闻聚合模块 — 多源新闻抓取、去重、分类、板块增强、推送."""

from stock_watcher.news.base import BaseNewsFetcher
from stock_watcher.news.eastmoney_stock import EastMoneyStockNewsFetcher
from stock_watcher.news.eastmoney_flash import EastMoneyFlashNewsFetcher
from stock_watcher.news.sina_flash import SinaFlashNewsFetcher
from stock_watcher.news.sina_roll import SinaRollNewsFetcher
from stock_watcher.news.sina_stock import SinaStockNewsFetcher
from stock_watcher.news.cls_telegraph import CLSTelegraphFetcher
from stock_watcher.news.yahoo_news import YahooNewsFetcher
from stock_watcher.news.aggregator import NewsAggregator
from stock_watcher.news.classifier import NewsClassifier, NewsLevel, ClassifyResult
from stock_watcher.news.card_builder import build_news_card, build_enriched_card
from stock_watcher.news.sector_matcher import SectorMatcher
from stock_watcher.news.trend_scorer import TrendScorer
from stock_watcher.news.trend_ranker import TrendRanker, RankedResult

__all__ = [
    "BaseNewsFetcher",
    "EastMoneyStockNewsFetcher",
    "EastMoneyFlashNewsFetcher",
    "SinaFlashNewsFetcher",
    "SinaRollNewsFetcher",
    "SinaStockNewsFetcher",
    "CLSTelegraphFetcher",
    "YahooNewsFetcher",
    "NewsAggregator",
    "NewsClassifier",
    "NewsLevel",
    "ClassifyResult",
    "build_news_card",
    "build_enriched_card",
    "SectorMatcher",
    "TrendScorer",
    "TrendRanker",
    "RankedResult",
]