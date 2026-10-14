package feishu

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"stock-watcher/internal/notify"
	"stock-watcher/internal/selection"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const maxPayloadBytes = 20 * 1024

type Failure = notify.DeliveryError

func ValidateWebhook(webhook string) error {
	u, err := url.Parse(webhook)
	if err != nil || u.Scheme != "https" || u.Host != "open.feishu.cn" ||
		!strings.HasPrefix(u.Path, "/open-apis/bot/v2/hook/") ||
		len(strings.TrimPrefix(u.Path, "/open-apis/bot/v2/hook/")) < 8 ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("invalid Feishu webhook")
	}
	return nil
}

func Sign(timestamp int64, secret string) string {
	h := hmac.New(sha256.New, []byte(strconv.FormatInt(timestamp, 10)+"\n"+secret))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

type Client struct {
	http    *http.Client
	name    string
	webhook string
	secret  string
	now     func() time.Time
}

func NewClient(webhook, secret string, timeout time.Duration) *Client {
	return NewNamedClient("feishu-main", webhook, secret, timeout)
}

func NewNamedClient(name, webhook, secret string, timeout time.Duration) *Client {
	if name == "" {
		name = "feishu-main"
	}
	return &Client{
		http:    &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		name:    name,
		webhook: webhook,
		secret:  secret,
		now:     time.Now,
	}
}

func escape(s string) string {
	return strings.NewReplacer("<", "〈", ">", "〉", "*", "＊", "[", "［", "]", "］", "\n", " ", "\r", " ").Replace(s)
}

func tags(values []string) string {
	labels := map[string]string{
		"historical_rule_breakout":        "放量突破有历史证据",
		"historical_rule_pullback":        "趋势回踩有历史证据",
		"historical_rule_momentum":        "相对强势有历史证据",
		"historical_rule_volume_recovery": "放量转强有历史证据",
		"rising_ma20":                     "20日均线上行", "stronger_than_pool": "近5日强于股池", "strong_close": "收盘接近日内高位",
		"insufficient_or_unprofitable_evidence": "历史样本或收益未达门槛", "extended_price": "短期涨幅或均线偏离过大",
		"large_daily_range": "日内波动过大", "upper_wick": "上影线较长", "cash_insufficient": "预算不足最小买入数量",
		"holdout_validation_not_passed": "独立验证未通过",
	}
	if len(values) == 0 {
		return "无"
	}
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = labels[value]
		if out[i] == "" {
			out[i] = value
		}
	}
	return escape(strings.Join(out, "、"))
}

func Card(value any) map[string]any {
	switch value := value.(type) {
	case notify.Message:
		return cardPayload(value, renderContent(value))
	case selection.Report:
		return selectionCard(value)
	default:
		return cardPayload(notify.Message{Version: 1, Title: "Notification"}, "")
	}
}

func selectionCard(r selection.Report) map[string]any {
	lines := []string{fmt.Sprintf("数据日期：%s · T+1：%s", r.AsOf, r.Target), fmt.Sprintf("龙头池 %d 只 · 有效日线 %d 只 · 排除 %d 只", r.PoolSize, r.Eligible, len(r.Excluded)), fmt.Sprintf("规则：%s · 分数 ≥ %d · 最多 %d 只", r.Strategy, r.MinScore, r.TopN)}
	lines = append(lines, strings.Join(r.Rules, "\n"))
	if r.Research != nil {
		s := r.Research.Parameters
		lines = []string{fmt.Sprintf("排名日期：%s · 计划买入日：%s", r.AsOf, r.Target), fmt.Sprintf("全池 %d 只 · 已排名 %d 只", r.PoolSize, len(r.Ranking)), strings.Join(r.Rules, "\n"), fmt.Sprintf("净收益口径 · 佣金 %.4f%% / 最低 %.2f 元 · 单股预算 %.0f 元 · 单边滑点 %.3f%%", s.CommissionRate*100, s.MinCommission, s.TradeCash, s.SlippageRate*100), "目标价和止损价以实际买入成交价重新计算；参考价不是买入报价。"}
		h := r.Research.Holdout
		lines = append(lines, fmt.Sprintf("独立验证 %s～%s · 规则通过 %d 个买入样本 · 净目标达标 %.1f%% · 已退出平均净收益 %.2f%%", h.From, h.Through, h.Recommended.Samples, h.Recommended.HitRate*100, h.Recommended.MeanNet))
		if !r.SelectionEnabled {
			lines = append(lines, "筛选尚未启用，以下仅为观察排名。")
		} else if len(r.Selected) == 0 {
			lines = append(lines, "本次无股票通过历史证据与风险门槛，以下仅为观察排名。")
		}
		// Show selected stocks first: higher ranked watch-only stocks must not hide them.
		displayed := map[string]bool{}
		for _, stock := range r.Selected {
			displayed[stock.Code] = true
		}
		order := append([]selection.Candidate(nil), r.Selected...)
		for i := range r.Ranking {
			x := &r.Ranking[i]
			if !displayed[x.Code] {
				order = append(order, selection.Candidate{Code: x.Code, Ranking: x})
			}
		}
		for _, stock := range order[:min(5, len(order))] {
			x := stock.Ranking
			if x == nil {
				continue
			}
			label := "观察"
			if x.Recommended && r.SelectionEnabled {
				label = "规则通过"
			}
			line := fmt.Sprintf("全池第%d · **%s %s** · 机会分 %.1f · %s\n依据：%s · 风险：%s", x.Rank, x.Code, escape(x.Name), x.Score, label, tags(x.Reasons), tags(x.Risks))
			if x.Evidence != nil {
				line += fmt.Sprintf("\n历史模拟：%d 样本/%d 个信号日 · 净目标达标 %.1f%% · 已退出平均净收益 %.2f%%", x.Evidence.Samples, x.Evidence.Dates, x.Evidence.HitRate*100, x.Evidence.MeanNet)
			}
			if x.Plan != nil {
				line += fmt.Sprintf("\n参考买入 %.2f → 净+2.5%%价 %.2f / 净+3%%价 %.2f / 止损 %.2f", x.Plan.EntryPrice, x.Plan.Target25, x.Plan.Target30, x.Plan.StopPrice)
			}
			lines = append(lines, line)
		}
		lines = append(lines, "历史模拟与机会分不等于未来获利概率；买入当天不能卖出，跳空时亏损可能超过止损幅度。", "事件 ID："+r.ID)
		return makeCard("A股龙头池 · 短线机会排名", lines)
	}
	if !r.SelectionEnabled {
		lines = append(lines, "筛选尚未启用，不生成推荐股。")
	} else if len(r.Selected) == 0 {
		lines = append(lines, "本次没有满足筛选规则的股票。")
	}
	for i, s := range r.Selected {
		lines = append(lines, fmt.Sprintf("%d. **%s %s** · %d 分 · 收盘 %.2f\nMA5/10/20 %.2f/%.2f/%.2f · RSI14 %.1f · 量比 %.2f · 连涨 %d 日", i+1, s.Code, escape(s.Name), s.Metrics.Score, s.Close, s.Metrics.MA5, s.Metrics.MA10, s.Metrics.MA20, s.Metrics.RSI14, s.Metrics.VolumeRatio, s.Metrics.UpDays))
	}
	if len(r.Excluded) > 0 {
		codes := []string{}
		for _, s := range r.Excluded[:minInt(len(r.Excluded), 10)] {
			codes = append(codes, s.Code+" ("+s.Reason+")")
		}
		lines = append(lines, "排除："+strings.Join(codes, ", "))
	}
	lines = append(lines, "仅为规则筛选结果，不保证次日表现。", "事件 ID："+r.ID)
	return makeCard("A股龙头池 · T+1 日线筛选", lines)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (c *Client) Name() string { return c.name }

func (c *Client) Close() {
	if c.http != nil {
		c.http.CloseIdleConnections()
	}
}

func renderContent(message notify.Message) string {
	lines := []string{}
	for _, section := range message.Sections {
		if section.Heading != "" {
			lines = append(lines, section.Heading)
		}
		lines = append(lines, section.Lines...)
	}
	return strings.Join(lines, "\n\n")
}

func makeCard(title string, lines []string) map[string]any {
	return cardPayload(notify.Message{Version: 1, Title: title, Sections: []notify.Section{{Lines: lines}}}, strings.Join(lines, "\n\n"))
}

func cardPayload(message notify.Message, content string) map[string]any {
	title := message.Title
	if title == "" {
		title = "Notification"
	}
	return map[string]any{
		"msg_type": "interactive",
		"card": map[string]any{
			"header": map[string]any{
				"template": "blue",
				"title":    map[string]any{"tag": "plain_text", "content": title},
			},
			"elements": []any{
				map[string]any{
					"tag":  "div",
					"text": map[string]any{"tag": "lark_md", "content": content},
				},
			},
		},
	}
}

func (c *Client) Send(ctx context.Context, message notify.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	timestamp := int64(0)
	if c.secret != "" {
		timestamp = c.now().Unix()
	}
	content := renderContent(message)
	data, err := c.marshal(message, content, timestamp)
	if err != nil {
		return &notify.DeliveryError{Channel: c.Name(), Kind: notify.Permanent, Reason: "payload_encode"}
	}
	if len(data) > maxPayloadBytes {
		content, data = c.fitPayload(message, timestamp, content)
		if len(data) > maxPayloadBytes {
			return &notify.DeliveryError{Channel: c.Name(), Kind: notify.Permanent, Reason: "payload_limit"}
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.webhook, bytes.NewReader(data))
	if err != nil {
		return &notify.DeliveryError{Channel: c.Name(), Kind: notify.Permanent, Reason: "request_build"}
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &notify.DeliveryError{Channel: c.Name(), Kind: notify.Retryable, Reason: "network"}
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 64*1024+1))
	if readErr != nil || len(body) > 64*1024 {
		return &notify.DeliveryError{Channel: c.Name(), Kind: notify.Unknown, Status: resp.StatusCode, Reason: "response_read"}
	}
	if resp.StatusCode != http.StatusOK {
		return &notify.DeliveryError{
			Channel:    c.Name(),
			Kind:       classifyHTTPStatus(resp.StatusCode),
			Status:     resp.StatusCode,
			RetryAfter: retryAfter(resp.Header.Get("Retry-After"), c.now()),
			Reason:     "http_status",
		}
	}
	var result struct {
		Code   *int `json:"code"`
		Legacy *int `json:"StatusCode"`
	}
	if json.Unmarshal(body, &result) != nil {
		return &notify.DeliveryError{Channel: c.Name(), Kind: notify.Unknown, Status: resp.StatusCode, Reason: "response_decode"}
	}
	code := result.Code
	if code == nil {
		code = result.Legacy
	}
	if code == nil {
		return &notify.DeliveryError{Channel: c.Name(), Kind: notify.Unknown, Status: resp.StatusCode, Reason: "missing_result_code"}
	}
	if *code == 0 {
		return nil
	}
	kind := notify.Permanent
	if *code == 11232 || *code == 99991400 {
		kind = notify.Retryable
	}
	return &notify.DeliveryError{Channel: c.Name(), Kind: kind, Status: resp.StatusCode, Reason: "business_rejected"}
}

func (c *Client) marshal(message notify.Message, content string, timestamp int64) ([]byte, error) {
	payload := cardPayload(message, content)
	if timestamp != 0 {
		payload["timestamp"] = strconv.FormatInt(timestamp, 10)
		payload["sign"] = Sign(timestamp, c.secret)
	}
	return json.Marshal(payload)
}

func (c *Client) fitPayload(message notify.Message, timestamp int64, content string) (string, []byte) {
	base := content
	suffix := ""
	if message.EventID != "" {
		marker := "事件 ID：" + message.EventID
		if index := strings.Index(base, marker); index >= 0 {
			base = strings.TrimSpace(base[:index])
		}
		suffix = "\n\n" + marker
	}
	low, high := 0, len(base)
	bestContent := ""
	var bestData []byte
	for low <= high {
		mid := (low + high) / 2
		candidate := truncateUTF8(base, mid) + suffix
		data, err := c.marshal(message, candidate, timestamp)
		if err != nil {
			return "", nil
		}
		if len(data) <= maxPayloadBytes {
			bestContent, bestData = candidate, data
			low = mid + 1
		} else {
			high = mid - 1
		}
	}
	return bestContent, bestData
}

func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	s = s[:maxBytes]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

func classifyHTTPStatus(status int) notify.Kind {
	switch {
	case status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500:
		return notify.Retryable
	case status == http.StatusUnauthorized || status == http.StatusForbidden || status >= 400:
		return notify.Permanent
	default:
		return notify.Unknown
	}
}

func retryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil && at.After(now) {
		return at.Sub(now)
	}
	return 0
}

var _ notify.Channel = (*Client)(nil)
