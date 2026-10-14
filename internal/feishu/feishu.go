package feishu

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"stock-watcher/internal/selection"
	"strconv"
	"strings"
	"time"
)

type Failure struct {
	Status int
	Code   int
	Kind   string
}

func (f *Failure) Error() string {
	return fmt.Sprintf("Feishu delivery failed (%s, HTTP %d, code %d)", f.Kind, f.Status, f.Code)
}
func (f *Failure) Retryable() bool {
	return f.Status == 0 || f.Status == 429 || f.Status >= 500 || f.Code == 11232 || f.Code == 99991400
}
func Sign(timestamp int64, secret string) string {
	h := hmac.New(sha256.New, []byte(strconv.FormatInt(timestamp, 10)+"\n"+secret))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

type Client struct {
	HTTP            *http.Client
	Webhook, Secret string
}

func NewClient(webhook, secret string, timeout time.Duration) *Client {
	return &Client{HTTP: &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, Webhook: webhook, Secret: secret}
}
func (c *Client) Close() { c.HTTP.CloseIdleConnections() }
func escape(s string) string {
	r := strings.NewReplacer("<", "〈", ">", "〉", "*", "＊", "[", "［", "]", "］", "\n", " ", "\r", " ")
	return r.Replace(s)
}
func Card(r selection.Report) map[string]any {
	lines := []string{fmt.Sprintf("数据日期：%s · T+1：%s", r.AsOf, r.Target), fmt.Sprintf("龙头池 %d 只 · 有效日线 %d 只 · 排除 %d 只", r.PoolSize, r.Eligible, len(r.Excluded)), fmt.Sprintf("规则：%s · 分数 ≥ %d · 最多 %d 只", r.Strategy, r.MinScore, r.TopN)}
	lines = append(lines, strings.Join(r.Rules, "\n"))
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
		for _, s := range r.Excluded[:min(len(r.Excluded), 10)] {
			codes = append(codes, s.Code+" ("+s.Reason+")")
		}
		lines = append(lines, "排除："+strings.Join(codes, ", "))
	}
	lines = append(lines, "仅为规则筛选结果，不保证次日表现。", "事件 ID："+r.ID)
	return map[string]any{"msg_type": "interactive", "card": map[string]any{"header": map[string]any{"template": "blue", "title": map[string]any{"tag": "plain_text", "content": "A股龙头池 · T+1 日线筛选"}}, "elements": []any{map[string]any{"tag": "div", "text": map[string]any{"tag": "lark_md", "content": strings.Join(lines, "\n\n")}}}}}
}
func (c *Client) Send(ctx context.Context, r selection.Report) error {
	payload := Card(r)
	if c.Secret != "" {
		ts := time.Now().Unix()
		payload["timestamp"] = strconv.FormatInt(ts, 10)
		payload["sign"] = Sign(ts, c.Secret)
	}
	data, err := json.Marshal(payload)
	if err != nil || len(data) > 20*1024 {
		return &Failure{Status: 400, Kind: "payload"}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Webhook, bytes.NewReader(data))
	if err != nil {
		return &Failure{Status: 400, Kind: "request"}
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &Failure{Kind: "network"}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return &Failure{Status: resp.StatusCode, Kind: "status"}
	}
	data, err = io.ReadAll(io.LimitReader(resp.Body, 64*1024+1))
	if err != nil || len(data) > 64*1024 {
		return &Failure{Kind: "response"}
	}
	var body struct {
		Code   *int `json:"code"`
		Legacy *int `json:"StatusCode"`
	}
	if json.Unmarshal(data, &body) != nil {
		return &Failure{Kind: "response"}
	}
	if body.Code != nil {
		if *body.Code == 0 {
			return nil
		}
		return &Failure{Status: 200, Code: *body.Code, Kind: "rejected"}
	}
	if body.Legacy != nil {
		if *body.Legacy == 0 {
			return nil
		}
		return &Failure{Status: 200, Code: *body.Legacy, Kind: "rejected"}
	}
	return &Failure{Status: 200, Kind: "missing_result_code"}
}
