package report

import (
	"stock-watcher/internal/selection"
	"strings"
	"testing"
)

func TestSelectionMessagePreservesCardContent(t *testing.T) {
	message, err := FromSelection(selection.Report{
		Version:          1,
		ID:               "event-1",
		AsOf:             "2026-10-13",
		Target:           "2026-10-14",
		Strategy:         "leader_daily_v1",
		SelectionEnabled: true,
		MinScore:         70,
		TopN:             5,
		PoolSize:         2,
		Eligible:         2,
		Rules:            []string{"规则一", "规则二"},
		Selected: []selection.Candidate{{
			Code:  "sz000001",
			Name:  "测试*股票",
			Close: 10,
			Metrics: selection.Metrics{
				Score: 80, MA5: 10, MA10: 9, MA20: 8, RSI14: 60, VolumeRatio: 1.5, UpDays: 3,
			},
		}},
		Excluded: []selection.Excluded{{Code: "sh600000", Reason: "special_treatment"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if message.Version != 1 || message.EventID != "event-1" || message.Title != "A股龙头池 · T+1 日线筛选" {
		t.Fatalf("unexpected message metadata: %+v", message)
	}
	content := strings.Join(message.Sections[0].Lines, "\n\n")
	for _, want := range []string{
		"数据日期：2026-10-13 · T+1：2026-10-14",
		"规则：leader_daily_v1 · 分数 ≥ 70 · 最多 5 只",
		"**sz000001 测试＊股票**",
		"排除：sh600000 (special_treatment)",
		"事件 ID：event-1",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("message content lacks %q: %s", want, content)
		}
	}
}

func TestSelectionMessageHasExpiry(t *testing.T) {
	message, err := FromSelection(selection.Report{Version: 1, ID: "event", AsOf: "2026-10-13", Target: "2026-10-14"})
	if err != nil {
		t.Fatal(err)
	}
	if message.ExpiresAt.IsZero() || message.ExpiresAt.Location() == nil {
		t.Fatal("message expiry is missing")
	}
	if message.ExpiresAt.Format("2006-01-02 15:04") != "2026-10-14 09:30" {
		t.Fatalf("unexpected expiry %s", message.ExpiresAt)
	}
}
