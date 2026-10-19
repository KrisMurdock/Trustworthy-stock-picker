package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"stock-watcher/internal/config"
	"stock-watcher/internal/notify"
	reportmessage "stock-watcher/internal/notify/report"
	"stock-watcher/internal/selection"
	"stock-watcher/internal/strategy"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestOfficialSignatureAlgorithm(t *testing.T) {
	// Fixture computed independently with HMAC-SHA256(key=timestamp+newline+secret, empty message).
	if got := Sign(1599360473, "test-secret"); got != "wSds2BzzFIIGf/WrhUO+NI1q/9j+FRJd3JNHKAq0NZY=" {
		t.Fatalf("signature %s", got)
	}
}

func TestStructuredCardRendersFieldsAndEventID(t *testing.T) {
	payload := Card(notify.Message{
		Version: 2,
		EventID: "event-v2",
		Title:   "title",
		Sections: []notify.Section{
			{Heading: "摘要", Fields: []notify.Field{{Label: "日期", Value: "2026-10-13"}, {Label: "状态", Value: "有推荐"}}},
			{Heading: "第 1 名 · sz000001 测试", Fields: []notify.Field{{Label: "机会分", Value: "82.5"}, {Label: "风险", Value: "上影线较长"}}},
		},
	})
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	card, ok := envelope["card"].(map[string]any)
	if !ok {
		t.Fatalf("card missing from payload: %s", data)
	}
	elements, ok := card["elements"].([]any)
	if !ok {
		t.Fatalf("elements missing from card: %s", data)
	}
	fieldCount := 0
	foundEventID := false
	for _, rawElement := range elements {
		element, ok := rawElement.(map[string]any)
		if !ok {
			continue
		}
		fields, _ := element["fields"].([]any)
		for _, rawField := range fields {
			field, _ := rawField.(map[string]any)
			text, _ := field["text"].(map[string]any)
			if field["is_short"] != true || text["tag"] != "lark_md" {
				t.Fatalf("field is not rendered as a short lark markdown field: %+v", field)
			}
			fieldCount++
		}
		notes, _ := element["elements"].([]any)
		for _, rawNote := range notes {
			note, _ := rawNote.(map[string]any)
			if note["tag"] == "plain_text" && strings.Contains(note["content"].(string), "event-v2") {
				foundEventID = true
			}
		}
	}
	if fieldCount != 4 || !foundEventID {
		t.Fatalf("structured card missing fields or event id: fields=%d event=%v payload=%s", fieldCount, foundEventID, data)
	}
}

func TestStructuredCardRendersNativeTable(t *testing.T) {
	payload := Card(notify.Message{
		Version: 2,
		EventID: "event-table",
		Title:   "title",
		Sections: []notify.Section{{
			Heading: "决策",
			Lines:   []string{"先看结论"},
			Table: &notify.Table{
				PageSize:          10,
				FreezeFirstColumn: true,
				Columns: []notify.TableColumn{
					{Key: "stock", Label: "标的", Kind: notify.TableText, Width: "26%"},
					{Key: "status", Label: "状态", Kind: notify.TableStatus},
					{Key: "change", Label: "涨跌", Kind: notify.TableChange},
				},
				Rows: [][]notify.TableCell{
					{{Text: "1 · sz000001 名称"}, {Text: "可执行"}, {Text: "+1.20%", Tone: notify.TonePositive}},
					{{Text: ""}, {Text: "观察"}, {Text: "-0.50%", Tone: notify.ToneNegative}},
					{{Text: "sz000002"}, {Text: "未启用"}},
				},
			},
		}},
	})
	card, elements := decodeCard(t, payload)
	tableIndex := -1
	lineIndex := -1
	var table map[string]any
	for i, rawElement := range elements {
		element, ok := rawElement.(map[string]any)
		if !ok {
			continue
		}
		if text, ok := element["text"].(map[string]any); ok && text["content"] == "先看结论" {
			lineIndex = i
		}
		if element["tag"] == "table" {
			tableIndex = i
			table = element
		}
	}
	if table == nil || lineIndex < 0 || tableIndex < lineIndex {
		t.Fatalf("table missing or ordered before conclusion line: %s", card)
	}
	if table["page_size"] != float64(10) || table["row_height"] != "middle" || table["freeze_first_column"] != true {
		t.Fatalf("unexpected table layout: %+v", table)
	}
	headerStyle, _ := table["header_style"].(map[string]any)
	if headerStyle["background_style"] != "grey" || headerStyle["bold"] != true || headerStyle["lines"] != float64(2) {
		t.Fatalf("unexpected table header style: %+v", headerStyle)
	}
	columns, _ := table["columns"].([]any)
	if len(columns) != 3 {
		t.Fatalf("unexpected columns: %+v", columns)
	}
	assertColumn := func(index int, name, display, dataType, width string) {
		t.Helper()
		column, _ := columns[index].(map[string]any)
		actualWidth, _ := column["width"].(string)
		if column["name"] != name || column["display_name"] != display || column["data_type"] != dataType || actualWidth != width {
			t.Fatalf("unexpected column %d: %+v", index, column)
		}
	}
	assertColumn(0, "stock", "标的", "text", "26%")
	assertColumn(1, "status", "状态", "options", "")
	assertColumn(2, "change", "涨跌", "lark_md", "")
	rows, _ := table["rows"].([]any)
	if len(rows) != 3 {
		t.Fatalf("unexpected rows: %+v", rows)
	}
	firstRow, _ := rows[0].(map[string]any)
	statusCell, _ := firstRow["status"].([]any)
	status, _ := statusCell[0].(map[string]any)
	if firstRow["stock"] != "1 · sz000001 名称" || status["text"] != "可执行" || status["color"] != "green" || firstRow["change"] != "<font color=red>+1.20%</font>" {
		t.Fatalf("unexpected first row: %+v", firstRow)
	}
	secondRow, _ := rows[1].(map[string]any)
	statusCell, _ = secondRow["status"].([]any)
	status, _ = statusCell[0].(map[string]any)
	if secondRow["stock"] != "—" || status["text"] != "观察" || status["color"] != "neutral" || secondRow["change"] != "<font color=green>-0.50%</font>" {
		t.Fatalf("unexpected second row: %+v", secondRow)
	}
	thirdRow, _ := rows[2].(map[string]any)
	statusCell, _ = thirdRow["status"].([]any)
	status, _ = statusCell[0].(map[string]any)
	if status["text"] != "未启用" || status["color"] != "neutral" || thirdRow["change"] != "—" {
		t.Fatalf("unexpected third row: %+v", thirdRow)
	}
}

func TestTableLayoutDefaults(t *testing.T) {
	table := tableElement(&notify.Table{
		Columns: []notify.TableColumn{{Key: "stock", Label: "标的", Kind: notify.TableText}},
		Rows:    [][]notify.TableCell{{{Text: "sz000001"}}},
	})
	if table["page_size"] != 5 || table["row_height"] != "middle" || table["freeze_first_column"] != false {
		t.Fatalf("unexpected table defaults: %+v", table)
	}
}

func TestStructuredCardUsesMessageTemplate(t *testing.T) {
	for _, tt := range []struct{ template, want string }{
		{"", "blue"},
		{"green", "green"},
		{"blue", "blue"},
		{"grey", "grey"},
		{"orange", "orange"},
	} {
		t.Run(tt.template, func(t *testing.T) {
			card, _ := decodeCard(t, Card(notify.Message{Version: 2, Template: tt.template, Title: "title"}))
			header, _ := card["header"].(map[string]any)
			if header["template"] != tt.want {
				t.Fatalf("template=%q got=%v want=%q", tt.template, header["template"], tt.want)
			}
		})
	}
}

func TestStructuredCardRendersDisplayEventIDAndPartLabel(t *testing.T) {
	_, elements := decodeCard(t, Card(notify.Message{
		Version:        2,
		EventID:        "internal-event",
		DisplayEventID: "SW-20261019-1",
		PartLabel:      "主卡",
		Title:          "title",
	}))
	marker := ""
	for _, rawElement := range elements {
		element, _ := rawElement.(map[string]any)
		notes, _ := element["elements"].([]any)
		for _, rawNote := range notes {
			note, _ := rawNote.(map[string]any)
			if note["tag"] == "plain_text" {
				marker, _ = note["content"].(string)
			}
		}
	}
	if marker != "事件 ID：SW-20261019-1 · 主卡" {
		t.Fatalf("unexpected event marker %q", marker)
	}
}

func TestDecisionCardsFitFeishuPayloadLimit(t *testing.T) {
	parameters := config.DefaultStrategy()
	parameters.Enabled = true
	research := &strategy.Research{
		Version:         1,
		Strategy:        "short_horizon_v1",
		AsOf:            "2026-10-16",
		Parameters:      parameters,
		TrainingThrough: "2026-09-30",
		SampleCount:     120,
		Development:     strategy.Evaluation{From: "2026-01-02", Through: "2026-08-31", SignalDates: 80, Method: "rolling"},
		Holdout:         strategy.Evaluation{From: "2026-09-01", Through: "2026-09-30", SignalDates: 20, Method: "frozen_holdout"},
		Rules: []strategy.Stats{
			{Rule: "baseline", Target: parameters.TargetNetPct, Horizon: parameters.MaxHoldingDays, Samples: 500, Dates: 100, Resolved: 480, MeanNet: .32, HitRate: .41},
			{Rule: "momentum", Target: parameters.TargetNetPct, Horizon: parameters.MaxHoldingDays, Samples: 180, Dates: 60, Resolved: 176, MeanNet: .85, HitRate: .52, WorstNet: -2.1, LossLimitPct: 3},
		},
	}
	r := selection.Report{
		Version: 2, ID: "payload-limit", AsOf: "2026-10-16", Target: "2026-10-19",
		SelectionEnabled: true, Strategy: "short_horizon_v1", PoolSize: 20, Eligible: 20, TopN: 10, Research: research,
	}
	for index := 1; index <= 10; index++ {
		candidate := strategy.Candidate{
			Code: fmt.Sprintf("sz%06d", index), Name: fmt.Sprintf("候选股票%d", index), Rank: index,
			Score: 90 - float64(index), Recommended: true,
			Rules: []string{"baseline", "momentum"}, Reasons: []string{"historical_rule_breakout", "rising_ma20", "strong_close"},
			Risks:    []string{"upper_wick", "large_daily_range", "extended_price"},
			Evidence: &strategy.Stats{Samples: 180, Dates: 60, Resolved: 176, HitRate: .52, MeanNet: .85, LowerBound: .12},
			Plan:     &strategy.Plan{EntryPrice: 10 + float64(index)/100, TakeProfit: 10.5 + float64(index)/100, StopPrice: 9.7},
			ScoreBreakdown: &strategy.ScoreBreakdown{
				Formula: fmt.Sprintf("%d + 4.2(日线17×0.25) + 10(量能适中) + 8(30m结构通过) + 5(15m底分型) - 2(上影线) = %.1f", 60-index, 80-float64(index)),
			},
		}
		r.Ranking = append(r.Ranking, candidate)
		ranking := &r.Ranking[len(r.Ranking)-1]
		r.Selected = append(r.Selected, selection.Candidate{Code: candidate.Code, Name: candidate.Name, Ranking: ranking})
	}
	for index := 1; index <= 12; index++ {
		r.Excluded = append(r.Excluded, selection.Excluded{Code: fmt.Sprintf("ex%06d", index), Reason: "missing_data", LastDate: "2026-10-16"})
	}
	for index := 1; index <= 10; index++ {
		r.Filtered = append(r.Filtered, selection.Excluded{Code: fmt.Sprintf("flt%06d", index), Reason: "score_below_threshold"})
	}

	cards, err := reportmessage.FromSelection(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(cards.Details) != 2 {
		t.Fatalf("details = %d, want 2 pages", len(cards.Details))
	}
	messages := append([]notify.Message{cards.Main}, cards.Details...)
	for _, message := range messages {
		data, err := json.Marshal(Card(message))
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s payload = %d bytes", message.PartLabel, len(data))
		if len(data) > maxPayloadBytes {
			t.Fatalf("%s payload = %d bytes, limit %d", message.PartLabel, len(data), maxPayloadBytes)
		}
	}
}

func TestStructuredCardKeepsLongLinesFullWidth(t *testing.T) {
	longLine := strings.Repeat("长", 4000)
	_, elements := decodeCard(t, Card(notify.Message{
		Version:  2,
		EventID:  "event-line",
		Title:    "title",
		Sections: []notify.Section{{Lines: []string{longLine}}},
	}))
	for _, rawElement := range elements {
		element, _ := rawElement.(map[string]any)
		text, _ := element["text"].(map[string]any)
		if text["content"] != longLine {
			continue
		}
		if _, hasFields := element["fields"]; hasFields || text["tag"] != "lark_md" {
			t.Fatalf("long line was not rendered as a full-width div: %+v", element)
		}
		return
	}
	t.Fatal("long line was not rendered")
}

func TestRenderContentFallsBackToPlainTextTable(t *testing.T) {
	content := renderContent(notify.Message{
		Version: 2,
		Sections: []notify.Section{{
			Heading: "决策",
			Table: &notify.Table{
				Columns: []notify.TableColumn{
					{Key: "stock", Label: "标的"},
					{Key: "status", Label: "状态"},
					{Key: "change", Label: "涨跌"},
				},
				Rows: [][]notify.TableCell{
					{{Text: "sz000001"}, {Text: "可执行"}, {Text: "+1.20%"}},
					{{Text: ""}, {Text: "观察"}, {Text: "-0.50%"}},
				},
			},
		}},
	})
	for _, want := range []string{"决策", "标的 | 状态 | 涨跌", "sz000001 | 可执行 | +1.20%", "— | 观察 | -0.50%"} {
		if !strings.Contains(content, want) {
			t.Fatalf("plain text fallback missing %q: %q", want, content)
		}
	}
}

func TestVersionOneCardKeepsLegacyMarkdownRendering(t *testing.T) {
	payload := Card(notify.Message{
		Version:  1,
		EventID:  "event-v1",
		Title:    "title",
		Sections: []notify.Section{{Heading: "heading", Lines: []string{"first", "second", "事件 ID：event-v1"}}},
	})
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"lark_md", "heading", "first", "second", "event-v1"} {
		if !strings.Contains(text, want) {
			t.Fatalf("legacy card missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, "\"fields\"") {
		t.Fatalf("version one card changed layout: %s", text)
	}
}
func TestDeliveryAcknowledgementAndPermissions(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		status     int
		ok, retry  bool
	}{{"success", `{"code":0}`, 200, true, false}, {"legacy", `{"StatusCode":0}`, 200, true, false}, {"unknown_result", `{}`, 200, false, true}, {"bad_signature", `{"code":19021}`, 200, false, false}, {"permission", `{}`, 403, false, false}, {"limit", `{"code":11232}`, 200, false, true}, {"server", `{}`, 503, false, true}} {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var p map[string]any
				json.NewDecoder(r.Body).Decode(&p)
				ts, _ := strconv.ParseInt(p["timestamp"].(string), 10, 64)
				if p["sign"] != Sign(ts, "test-secret") {
					t.Error("incorrect signature payload")
				}
				w.WriteHeader(tt.status)
				w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			c := NewClient(srv.URL, "test-secret", time.Second)
			defer c.Close()
			err := c.Send(context.Background(), notify.Message{Version: 1, EventID: "event", Title: "title", Sections: []notify.Section{{Lines: []string{"line"}}}})
			if (err == nil) != tt.ok {
				t.Fatalf("error=%v", err)
			}
			if err != nil {
				var f *Failure
				if !errors.As(err, &f) || f.Retryable() != tt.retry {
					t.Fatalf("retry mismatch %v", err)
				}
			}
		})
	}
}

func TestResponseClassificationAndRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "17")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "", time.Second)
	defer c.Close()
	err := c.Send(context.Background(), notify.Message{Version: 1, EventID: "event", Sections: []notify.Section{{Lines: []string{"line"}}}})
	var failure *notify.DeliveryError
	if !errors.As(err, &failure) || failure.Kind != notify.Retryable || failure.RetryAfter != 17*time.Second {
		t.Fatalf("unexpected 429 failure: %#v", err)
	}
}

func TestUnexpectedSuccessStatusIsUnknown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"code":0}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "", time.Second)
	defer c.Close()
	err := c.Send(context.Background(), notify.Message{Version: 1, EventID: "event", Sections: []notify.Section{{Lines: []string{"line"}}}})
	var failure *notify.DeliveryError
	if !errors.As(err, &failure) || failure.Kind != notify.Unknown || failure.Status != http.StatusAccepted {
		t.Fatalf("unexpected 202 failure: %#v", err)
	}
}

func TestOversizedCardIsTrimmedBeforeRequest(t *testing.T) {
	var calls int
	var received int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		received = len(data)
		w.Write([]byte(`{"code":0}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "", time.Second)
	defer c.Close()
	err := c.Send(context.Background(), notify.Message{
		Version:  1,
		EventID:  "event",
		Sections: []notify.Section{{Lines: []string{strings.Repeat("长", 30000), "事件 ID：event"}}},
	})
	if err != nil || calls != 1 || received > maxPayloadBytes {
		t.Fatalf("oversized payload was not trimmed: err=%v calls=%d bytes=%d", err, calls, received)
	}
}

func TestOversizedStructuredCardDropsOptionalSectionsAndPreservesSummary(t *testing.T) {
	var received []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		received, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte("{\"code\":0}"))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "", time.Second)
	defer c.Close()
	err := c.Send(context.Background(), notify.Message{
		Version: 2,
		EventID: "event-structured",
		Title:   "title",
		Sections: []notify.Section{
			{Heading: "摘要", Fields: []notify.Field{{Label: "状态", Value: "保留摘要"}}},
			{Heading: "首选股票", Optional: true, Fields: []notify.Field{{Label: "名称", Value: "保留候选"}}},
			{Heading: "长尾股票", Optional: true, Fields: []notify.Field{{Label: "详情", Value: strings.Repeat("长", 30000)}}},
		},
	})
	if err != nil {
		t.Fatalf("oversized structured card was not trimmed: %v", err)
	}
	if len(received) == 0 || len(received) > maxPayloadBytes {
		t.Fatalf("unexpected structured payload size %d", len(received))
	}
	text := string(received)
	for _, want := range []string{"保留摘要", "保留候选", "event-structured"} {
		if !strings.Contains(text, want) {
			t.Fatalf("trimmed card lost %q: %s", want, text)
		}
	}
	if strings.Contains(text, "长尾股票") || strings.Contains(text, strings.Repeat("长", 100)) {
		t.Fatalf("trimmed card retained oversized optional section: size=%d", len(received))
	}
}

func TestTrimNoneDoesNotDropOptionalSections(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Write([]byte("{\"code\":0}"))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "", time.Second)
	defer c.Close()
	err := c.Send(context.Background(), notify.Message{
		Version:    2,
		EventID:    "event-no-trim",
		Title:      "title",
		TrimPolicy: notify.TrimNone,
		Sections: []notify.Section{
			{Heading: "摘要", Fields: []notify.Field{{Label: "状态", Value: "保留摘要"}}},
			{Heading: "长尾股票", Optional: true, Lines: []string{strings.Repeat("长", 30000)}},
		},
	})
	var failure *notify.DeliveryError
	if !errors.As(err, &failure) || failure.Kind != notify.Permanent || failure.Reason != "payload_limit" || calls != 0 {
		t.Fatalf("trim-none oversized card should fail without trimming or request: err=%v calls=%d", err, calls)
	}
}

func TestOversizedMandatoryStructuredCardFailsBeforeRequest(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Write([]byte("{\"code\":0}"))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "", time.Second)
	defer c.Close()
	err := c.Send(context.Background(), notify.Message{
		Version: 2,
		EventID: "core-event",
		Title:   "title",
		Sections: []notify.Section{{
			Heading: "摘要",
			Fields:  []notify.Field{{Label: "必要字段", Value: strings.Repeat("x", maxPayloadBytes)}},
		}},
	})
	var failure *notify.DeliveryError
	if !errors.As(err, &failure) || failure.Kind != notify.Permanent || failure.Reason != "payload_limit" || calls != 0 {
		t.Fatalf("mandatory oversized card should fail before request: err=%v calls=%d", err, calls)
	}
}

func TestWebhookTimeoutIsRetryable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		_, _ = w.Write([]byte("{\"code\":0}"))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "", 10*time.Millisecond)
	defer c.Close()
	err := c.Send(context.Background(), notify.Message{
		Version:  2,
		EventID:  "timeout-event",
		Sections: []notify.Section{{Fields: []notify.Field{{Label: "状态", Value: "测试"}}}},
	})
	var failure *notify.DeliveryError
	if !errors.As(err, &failure) || failure.Kind != notify.Retryable {
		t.Fatalf("timeout should be retryable, got %v", err)
	}
}

func TestCardRendersMessageWithoutSelectionDependency(t *testing.T) {
	payload := Card(notify.Message{
		Version:  1,
		EventID:  "event-1",
		Title:    "title",
		Sections: []notify.Section{{Heading: "heading", Lines: []string{"first", "second"}}},
	})
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"\"title\":{\"content\":\"title\"", "heading", "first", "second"} {
		if !strings.Contains(text, want) {
			t.Fatalf("card does not contain %q: %s", want, text)
		}
	}
}

func TestCardRendersSelectionReportMainCard(t *testing.T) {
	_, elements := decodeCard(t, Card(selection.Report{
		Version: 2,
		ID:      "selection-event",
		AsOf:    "2026-10-16",
		Target:  "2026-10-19",
	}))
	marker := ""
	for _, rawElement := range elements {
		element, _ := rawElement.(map[string]any)
		notes, _ := element["elements"].([]any)
		for _, rawNote := range notes {
			note, _ := rawNote.(map[string]any)
			if note["tag"] == "plain_text" {
				marker, _ = note["content"].(string)
			}
		}
	}
	if marker != "事件 ID：selection-event · 主卡" {
		t.Fatalf("selection report did not render its main card: marker=%q", marker)
	}
}

func decodeCard(t *testing.T, payload map[string]any) (map[string]any, []any) {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	card, ok := envelope["card"].(map[string]any)
	if !ok {
		t.Fatalf("card missing from payload: %s", data)
	}
	elements, ok := card["elements"].([]any)
	if !ok {
		t.Fatalf("elements missing from card: %s", data)
	}
	return card, elements
}
