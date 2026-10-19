package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"stock-watcher/internal/market"
	"stock-watcher/internal/selection"
	"testing"
	"time"
)

type minuteReplayEngine struct{}

func (minuteReplayEngine) Descriptor() selection.EngineDescriptor {
	return selection.EngineDescriptor{ID: "leader_daily_v1", Version: 1, Periods: []string{"day", "5"}, Adjust: "none", FinalOnly: true, MissingPolicy: "exclude"}
}
func (minuteReplayEngine) Analyze(_ context.Context, in selection.Input) (selection.Report, error) {
	if len(in.Minutes) != 1 || len(in.Minutes[0].Bars) != 1 {
		return selection.Report{}, fmt.Errorf("expected one final as-of minute, got %+v", in.Minutes)
	}
	return selection.Report{Version: 1, ID: fmt.Sprintf("minute-%.1f", in.Minutes[0].Bars[0].Close), AsOf: in.AsOf, Target: in.Target, GeneratedAt: in.Now, Adjust: "none", Strategy: "leader_daily_v1", SelectionEnabled: in.Config.Selection.Enabled}, nil
}

func TestReplayFreezesChanMinutesAcrossLaterCorrections(t *testing.T) {
	m, _, _ := fixture(t)
	listingDatabase(t, m)
	m.Config.Strategy.Enabled = true
	m.BuildVersion = "merge-test"
	m.Engine = minuteReplayEngine{}
	ctx := context.Background()
	stock := m.Config.Stocks[0]
	day := time.Date(2026, 10, 13, 9, 35, 0, 0, market.Shanghai)
	future := day.AddDate(0, 0, 1)
	h := market.MinuteHistory{Version: 1, Code: stock.Code, Name: stock.Name, Source: "tencent", Period: 5, BasePeriod: 5, Adjust: "none", VolumeUnit: "provider_raw", FetchedAt: future.Add(time.Hour)}
	for i, end := range []time.Time{day, day.Add(5 * time.Minute), future} {
		h.Bars = append(h.Bars, market.MinuteBar{End: end, Open: 10, Close: 10, High: 12, Low: 9, Volume: 100, Final: i != 1})
	}
	save := func() {
		t.Helper()
		if err := m.Market.SaveMinutes(ctx, stock, future, h.FetchedAt, &h, "partial", "", false); err != nil {
			t.Fatal(err)
		}
	}
	save()
	if _, err := m.reportMinutes(ctx, []market.History{{Code: stock.Code}}, "2026-10-12"); err == nil {
		t.Fatal("future-only minutes silently disabled the gate")
	}
	first, err := m.Sync(ctx, "2026-10-13")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != "minute-10.0" {
		t.Fatal("minute gate not applied", first.ID)
	}
	h.Bars[0].Close = 11
	save()
	second, err := m.Sync(ctx, "2026-10-13")
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != "minute-11.0" || second.InputID == first.InputID {
		t.Fatal("minute correction did not produce distinct input", second.ID)
	}
	replay, err := m.Replay(ctx, first.Revision)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(first)
	after, _ := json.Marshal(replay)
	if string(before) != string(after) {
		t.Fatalf("replay used current minute data\n%s\n%s", before, after)
	}
}
