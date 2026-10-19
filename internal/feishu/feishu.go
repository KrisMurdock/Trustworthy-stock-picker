package feishu

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/notify"
	reportmessage "github.com/KrisMurdock/Trustworthy-stock-picker/internal/notify/report"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/selection"
	"io"
	"net/http"
	"net/url"
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

func Card(value any) map[string]any {
	switch value := value.(type) {
	case notify.Message:
		return cardPayload(value, renderContent(value))
	case selection.Report:
		cards, err := reportmessage.FromSelection(value)
		if err == nil {
			message := cards.Main
			return cardPayload(message, renderContent(message))
		}
	}
	return cardPayload(notify.Message{Version: 1, Title: "Notification"}, "")
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
		if section.Table != nil {
			tableLines := make([]string, 0, len(section.Table.Rows)+1)
			headers := make([]string, 0, len(section.Table.Columns))
			for _, column := range section.Table.Columns {
				headers = append(headers, column.Label)
			}
			if len(headers) > 0 {
				tableLines = append(tableLines, strings.Join(headers, " | "))
			}
			for _, row := range section.Table.Rows {
				cellCount := len(section.Table.Columns)
				if cellCount == 0 {
					cellCount = len(row)
				}
				cells := make([]string, 0, cellCount)
				for i := 0; i < cellCount; i++ {
					text := ""
					if i < len(row) {
						text = escape(row[i].Text)
					}
					if text == "" {
						text = "—"
					}
					cells = append(cells, text)
				}
				tableLines = append(tableLines, strings.Join(cells, " | "))
			}
			if len(tableLines) > 0 {
				lines = append(lines, strings.Join(tableLines, "\n"))
			}
		}
	}
	return strings.Join(lines, "\n\n")
}

func structuredElements(message notify.Message) []any {
	elements := make([]any, 0, len(message.Sections)*2+1)
	for i, section := range message.Sections {
		if i > 0 {
			elements = append(elements, map[string]any{"tag": "hr"})
		}
		if section.Heading != "" {
			elements = append(elements, map[string]any{
				"tag":  "div",
				"text": map[string]any{"tag": "lark_md", "content": "**" + escape(section.Heading) + "**"},
			})
		}
		for start := 0; start < len(section.Fields); start += 10 {
			end := min(start+10, len(section.Fields))
			fields := make([]any, 0, end-start)
			for _, field := range section.Fields[start:end] {
				fields = append(fields, map[string]any{
					"is_short": true,
					"text": map[string]any{
						"tag":     "lark_md",
						"content": "**" + escape(field.Label) + "**\n" + escape(field.Value),
					},
				})
			}
			elements = append(elements, map[string]any{"tag": "div", "fields": fields})
		}
		for _, line := range section.Lines {
			elements = append(elements, map[string]any{
				"tag":  "div",
				"text": map[string]any{"tag": "lark_md", "content": escape(line)},
			})
		}
		if section.Table != nil {
			elements = append(elements, tableElement(section.Table))
		}
	}
	if marker := eventMarker(message); marker != "" {
		elements = append(elements, map[string]any{
			"tag":      "note",
			"elements": []any{map[string]any{"tag": "plain_text", "content": marker}},
		})
	}
	return elements
}

func tableElement(table *notify.Table) map[string]any {
	pageSize := table.PageSize
	if pageSize <= 0 {
		pageSize = 5
	}
	rowHeight := table.RowHeight
	if rowHeight == "" {
		rowHeight = "middle"
	}
	columns := make([]any, 0, len(table.Columns))
	for _, column := range table.Columns {
		rendered := map[string]any{
			"name":         column.Key,
			"display_name": escape(column.Label),
			"data_type":    tableDataType(column.Kind),
		}
		if column.Width != "" {
			rendered["width"] = column.Width
		}
		columns = append(columns, rendered)
	}
	rows := make([]any, 0, len(table.Rows))
	for _, row := range table.Rows {
		rendered := make(map[string]any, len(table.Columns))
		for i, column := range table.Columns {
			cell := notify.TableCell{}
			if i < len(row) {
				cell = row[i]
			}
			rendered[column.Key] = tableCellValue(column.Kind, cell)
		}
		rows = append(rows, rendered)
	}
	return map[string]any{
		"tag":                 "table",
		"page_size":           pageSize,
		"row_height":          rowHeight,
		"freeze_first_column": table.FreezeFirstColumn,
		"header_style": map[string]any{
			"background_style": "grey",
			"bold":             true,
			"lines":            2,
		},
		"columns": columns,
		"rows":    rows,
	}
}

func tableDataType(kind notify.TableColumnKind) string {
	switch kind {
	case notify.TableStatus:
		return "options"
	case notify.TableChange:
		return "lark_md"
	default:
		return "text"
	}
}

func tableCellValue(kind notify.TableColumnKind, cell notify.TableCell) any {
	text := escape(cell.Text)
	if text == "" {
		text = "—"
	}
	switch kind {
	case notify.TableStatus:
		color := "neutral"
		if strings.TrimSpace(cell.Text) == "可执行" {
			color = "green"
		}
		return []any{map[string]any{"text": text, "color": color}}
	case notify.TableChange:
		switch cell.Tone {
		case notify.TonePositive:
			return "<font color=red>" + text + "</font>"
		case notify.ToneNegative:
			return "<font color=green>" + text + "</font>"
		default:
			return text
		}
	default:
		return text
	}
}

func eventMarker(message notify.Message) string {
	display := message.DisplayEventID
	if display == "" {
		display = message.EventID
	}
	if display == "" {
		return ""
	}
	marker := "事件 ID：" + display
	if message.PartLabel != "" {
		marker += " · " + message.PartLabel
	}
	return marker
}

func cardTemplate(template string) string {
	switch template {
	case "green", "blue", "grey", "orange":
		return template
	default:
		return "blue"
	}
}

func cardPayload(message notify.Message, content string) map[string]any {
	title := message.Title
	if title == "" {
		title = "Notification"
	}
	elements := []any{map[string]any{
		"tag":  "div",
		"text": map[string]any{"tag": "lark_md", "content": content},
	}}
	if message.Version == 2 {
		elements = structuredElements(message)
	}
	return map[string]any{
		"msg_type": "interactive",
		"card": map[string]any{
			"header": map[string]any{
				"template": cardTemplate(message.Template),
				"title":    map[string]any{"tag": "plain_text", "content": title},
			},
			"elements": elements,
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
		if len(data) == 0 || len(data) > maxPayloadBytes {
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
	if message.Version == 2 {
		return "", c.fitStructuredPayload(message, timestamp)
	}
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

func (c *Client) fitStructuredPayload(message notify.Message, timestamp int64) []byte {
	if message.TrimPolicy == notify.TrimNone {
		data, err := c.marshal(message, "", timestamp)
		if err != nil {
			return nil
		}
		return data
	}
	candidate := message
	candidate.Sections = append([]notify.Section(nil), message.Sections...)
	for {
		data, err := c.marshal(candidate, "", timestamp)
		if err != nil {
			return nil
		}
		if len(data) <= maxPayloadBytes {
			return data
		}
		optional := -1
		for i := len(candidate.Sections) - 1; i >= 0; i-- {
			if candidate.Sections[i].Optional {
				optional = i
				break
			}
		}
		if optional < 0 {
			return data
		}
		candidate.Sections = append(candidate.Sections[:optional], candidate.Sections[optional+1:]...)
	}
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
