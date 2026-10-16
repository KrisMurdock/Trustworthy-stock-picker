# 新浪日线协议样本

2026-10-16 从新浪公开 `hisdata_klc2/klc_kl.js` 接口采集，去除响应末尾注释。
`sh600036` 为 5878 根长期历史，`sh688808` 为 115 根较新股票历史。

测试预期由 [AKShare 官方参考解码器](https://github.com/akfamily/akshare/blob/main/akshare/stock/cons.py)
中的 `hk_js_decode` 独立生成：逐行按 `date,open,high,low,close,volume` 拼接，
日期取 ISO 日，数值固定八位小数，行尾 LF，计算 SHA256。
因此 Go 测试不需要 Python、JavaScript 引擎或网络，也能核对全部 OHLCV。
