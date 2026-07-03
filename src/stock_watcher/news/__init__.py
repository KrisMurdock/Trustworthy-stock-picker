"""新闻聚合模块 — 多源新闻抓取、去重、分类、推送."""

from stock_watcher.news.base import BaseNewsFetcher
from stock_watcher.news.sina_roll import SinaRollNewsFetcher
from stock_watcher.news.yahoo_search import YahooSearchNewsFetcher
from stock_watcher.news.sina_stock import SinaStockNewsFetcher
from stock_watcher.news.aggregator import NewsAggregator
from stock_watcher.news.classifier import NewsClassifier, NewsLevel, ClassifyResult
from stock_watcher.news.card_builder import build_news_card

__all__ = [
    "BaseNewsFetcher",
    "SinaRollNewsFetcher",
    "YahooSearchNewsFetcher",
    "SinaStockNewsFetcher",
    "NewsAggregator",
    "NewsClassifier",
    "NewsLevel",
    "ClassifyResult",
    "build_news_card",
    "build_enriched_card",
]
