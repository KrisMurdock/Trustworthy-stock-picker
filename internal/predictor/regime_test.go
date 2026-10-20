package predictor

import (
	"fmt"
	"testing"
	"time"
)

func regimeDays(start time.Time, count int) []string {
	out := make([]string, count)
	for i := range out {
		out[i] = start.AddDate(0, 0, i).Format("2006-01-02")
	}
	return out
}

func TestBuildMarketRegimesUsesPriorTwentySessions(t *testing.T) {
	days := regimeDays(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), 25)
	bars := make([]MarketBar, 0, len(days))
	for i := 0; i < 25; i++ {
		close := 100.0
		if i == 20 {
			close = 102
		}
		bars = append(bars, MarketBar{Date: days[i], Close: close})
	}
	got := BuildMarketRegimes(days, bars)
	if got[days[20]] != MarketRange {
		t.Fatalf("day with flat prior MA20 = %q, want range", got[days[20]])
	}
	if got[days[21]] != MarketUp {
		t.Fatalf("day after +2%% close = %q, want up", got[days[21]])
	}
	if got[days[10]] != MarketUnknown {
		t.Fatalf("day before twenty sessions = %q, want unknown", got[days[10]])
	}
}

func TestBuildMarketRegimesUnknownOnGapsAndStaleData(t *testing.T) {
	days := regimeDays(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), 22)
	bars := make([]MarketBar, 0, len(days))
	for i := 0; i < 21; i++ {
		if i == 7 || i == 20 {
			continue
		}
		bars = append(bars, MarketBar{Date: days[i], Close: 100})
	}
	got := BuildMarketRegimes(days, bars)
	if got[days[21]] != MarketUnknown {
		t.Fatalf("missing session = %q, want unknown", got[days[21]])
	}
	if got[days[20]] != MarketUnknown {
		t.Fatalf("gap inside MA20 window = %q, want unknown", got[days[20]])
	}
}

func TestBuildMarketRegimesDownAndInvalidClose(t *testing.T) {
	days := regimeDays(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), 21)
	bars := make([]MarketBar, 0, len(days))
	for i := 0; i < 20; i++ {
		close := 100.0
		if i == 19 {
			close = 0
		}
		bars = append(bars, MarketBar{Date: days[i], Close: close})
	}
	got := BuildMarketRegimes(days, bars)
	if got[days[20]] != MarketUnknown {
		t.Fatalf("zero close = %q, want unknown", got[days[20]])
	}
	bars[19].Close = 98
	got = BuildMarketRegimes(days, bars)
	if got[days[20]] != MarketDown {
		t.Fatalf("down regime = %q, want down", got[days[20]])
	}
}

func TestBuildMarketRegimesIsDeterministic(t *testing.T) {
	days := regimeDays(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), 25)
	bars := make([]MarketBar, 0, len(days))
	for i, day := range days {
		bars = append(bars, MarketBar{Date: day, Close: 100 + float64(i%3)})
	}
	first := BuildMarketRegimes(days, bars)
	second := BuildMarketRegimes(days, bars)
	for _, day := range days {
		if first[day] != second[day] {
			t.Fatalf("%s changed: %q -> %q", day, first[day], second[day])
		}
	}
}

func BenchmarkBuildMarketRegimes(b *testing.B) {
	days := regimeDays(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), 1500)
	bars := make([]MarketBar, len(days))
	for i, day := range days {
		bars[i] = MarketBar{Date: day, Close: float64(3000 + i%100)}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = BuildMarketRegimes(days, bars)
	}
}

func ExampleBuildMarketRegimes() {
	days := []string{"2026-09-01", "2026-09-02"}
	fmt.Println(BuildMarketRegimes(days, nil)[days[0]])
	// Output: unknown
}
