package yangpullback

import (
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

func TestScanDetectsValidPattern(t *testing.T) {
	bars := validPatternBars()
	signals, err := Scan(testHistory(bars), testOptions(bars), ModePattern)
	if err != nil {
		t.Fatal(err)
	}
	if len(signals) != 1 {
		t.Fatalf("signals = %+v, want one", signals)
	}
	want := Signal{
		Version:      Version,
		Rule:         Rule,
		Code:         "sz000001",
		Date:         bars[59].Date,
		HighDate:     bars[56].Date,
		High:         12,
		AnchorDate:   bars[56].Date,
		AnchorLow:    9.8,
		PullbackDays: 2,
	}
	if signals[0] != want {
		t.Fatalf("signal = %+v, want %+v", signals[0], want)
	}
}

func TestScanRejectsPullbackBelowAnchorLow(t *testing.T) {
	bars := validPatternBars()
	setBar(bars, 58, 10, 9.5, 10, 9.3)
	setBar(bars, 59, 10, 11, 11.2, 9.9)

	for _, mode := range []Mode{ModePattern, ModeHoldOnly} {
		signals, err := Scan(testHistory(bars), testOptions(bars), mode)
		if err != nil {
			t.Fatal(err)
		}
		if len(signals) != 0 {
			t.Fatalf("mode %s accepted an undercut pullback: %+v", mode, signals)
		}
	}
}

func TestScanRejectsPullbackCloseAboveHigh(t *testing.T) {
	bars := validPatternBars()
	setBar(bars, 58, 12.2, 12.5, 12.6, 12.1)
	setBar(bars, 59, 12.6, 12.8, 12.9, 12.5)

	for _, mode := range []Mode{ModePattern, ModePullbackOnly, ModeHoldOnly} {
		signals, err := Scan(testHistory(bars), testOptions(bars), mode)
		if err != nil {
			t.Fatal(err)
		}
		if len(signals) != 0 {
			t.Fatalf("mode %s accepted a new pullback high: %+v", mode, signals)
		}
	}
}

func TestScanHighWindowIncludesTenthPriorBar(t *testing.T) {
	bars := validPatternBars()
	setBar(bars, 49, 10, 11, 13, 9.8)

	signals, err := Scan(testHistory(bars), testOptions(bars), ModePattern)
	if err != nil {
		t.Fatal(err)
	}
	if len(signals) != 0 {
		t.Fatalf("high outside the ten-session window was ignored: %+v", signals)
	}
}

func TestScanKeepsAnchorAcrossSmallPullbackYang(t *testing.T) {
	bars := flatBars(60)
	setBar(bars, 54, 10, 11, 11.5, 9.8)
	setBar(bars, 56, 11.5, 11.2, 12, 11)
	setBar(bars, 57, 10.5, 10.3, 10.8, 10.1)
	setBar(bars, 58, 10.2, 10.4, 10.6, 10)
	setBar(bars, 59, 10.6, 11.1, 11.2, 10.5)

	signals, err := Scan(testHistory(bars), testOptions(bars), ModePattern)
	if err != nil {
		t.Fatal(err)
	}
	if len(signals) != 1 {
		t.Fatalf("signals = %+v, want one", signals)
	}
	if signals[0].HighDate != bars[56].Date || signals[0].AnchorDate != bars[54].Date {
		t.Fatalf("small pullback yang reset the anchor: %+v", signals[0])
	}
}

func TestScanExpiresAfterMaximumPullback(t *testing.T) {
	bars := flatBars(61)
	setBar(bars, 53, 11, 11.5, 12, 9.8)
	setBar(bars, 59, 10.6, 10.4, 10.8, 10.2)
	setBar(bars, 60, 10.8, 11.2, 11.3, 10.7)

	signals, err := Scan(testHistory(bars), testOptions(bars), ModePattern)
	if err != nil {
		t.Fatal(err)
	}
	if len(signals) != 0 {
		t.Fatalf("sixth pullback day emitted a signal: %+v", signals)
	}
}

func TestScanEmitsFirstConfirmationPerSetup(t *testing.T) {
	bars := flatBars(61)
	setBar(bars, 56, 10, 11.5, 13, 9.8)
	setBar(bars, 57, 10.5, 10.4, 10.8, 10.1)
	setBar(bars, 58, 10.3, 10.6, 10.8, 10.2)
	setBar(bars, 59, 10.8, 11.2, 11.5, 10.6)
	setBar(bars, 60, 11.3, 11.9, 12.2, 11.2)

	signals, err := Scan(testHistory(bars), testOptions(bars), ModePattern)
	if err != nil {
		t.Fatal(err)
	}
	if len(signals) != 1 || signals[0].Date != bars[59].Date {
		t.Fatalf("setup emitted more than once: %+v", signals)
	}
}

func TestScanAllowsDifferentHighSetupsWithSameAnchor(t *testing.T) {
	bars := flatBars(61)
	setBar(bars, 54, 10, 11, 11.5, 9.8)
	setBar(bars, 56, 11.5, 11.2, 12, 11)
	setBar(bars, 57, 12, 11.8, 12.5, 11.5)
	setBar(bars, 58, 10.3, 10.6, 10.9, 10.2)
	setBar(bars, 59, 10.7, 11.2, 11.3, 10.6)
	setBar(bars, 60, 11.4, 11.8, 11.9, 11.3)

	signals, err := Scan(testHistory(bars), testOptions(bars), ModePattern)
	if err != nil {
		t.Fatal(err)
	}
	if len(signals) != 2 {
		t.Fatalf("signals = %+v, want two setups", signals)
	}
	if signals[0].AnchorDate != bars[54].Date || signals[1].AnchorDate != bars[54].Date {
		t.Fatalf("test did not preserve the shared anchor: %+v", signals)
	}
	if signals[0].HighDate != bars[56].Date || signals[1].HighDate != bars[57].Date {
		t.Fatalf("same-anchor setups were not kept distinct: %+v", signals)
	}
}

func TestScanUsesLastBullishBeforeHighOutsideLookbackWindow(t *testing.T) {
	bars := flatBars(64)
	setBar(bars, 50, 10, 11, 11.2, 9.9)
	setBar(bars, 60, 12.5, 12.2, 13, 12.1)
	setBar(bars, 61, 10.6, 10.5, 10.8, 10.2)
	setBar(bars, 62, 10.3, 10.4, 10.7, 10.1)
	setBar(bars, 63, 10.8, 11.5, 11.6, 10.7)

	signals, err := Scan(testHistory(bars), testOptions(bars), ModePattern)
	if err != nil {
		t.Fatal(err)
	}
	if len(signals) != 1 {
		t.Fatalf("signals = %+v, want one", signals)
	}
	if signals[0].HighDate != bars[60].Date || signals[0].AnchorDate != bars[50].Date {
		t.Fatalf("anchor was not found outside the high lookback: %+v", signals[0])
	}
}

func TestScanDoesNotLeakFutureBars(t *testing.T) {
	prefix := validPatternBars()
	prefixSignals, err := Scan(testHistory(prefix), testOptions(prefix), ModePattern)
	if err != nil {
		t.Fatal(err)
	}
	if len(prefixSignals) != 1 {
		t.Fatalf("prefix signals = %+v, want one", prefixSignals)
	}

	full := appendFutureBars(prefix, 5)
	fullSignals, err := Scan(testHistory(full), testOptions(full), ModePattern)
	if err != nil {
		t.Fatal(err)
	}
	for _, signal := range fullSignals {
		if signal.Date == prefixSignals[0].Date {
			if signal != prefixSignals[0] {
				t.Fatalf("future bars changed historical signal: got %+v want %+v", signal, prefixSignals[0])
			}
			return
		}
	}
	t.Fatalf("historical signal missing after appending future bars: %+v", fullSignals)
}

func TestScanModeDifferences(t *testing.T) {
	tests := []struct {
		name string
		bars func() []market.Bar
		want map[Mode]bool
	}{
		{
			name: "confirmed pattern",
			bars: validPatternBars,
			want: map[Mode]bool{ModePattern: true, ModePullbackOnly: true, ModeHoldOnly: true},
		},
		{
			name: "holds anchor without confirmation",
			bars: func() []market.Bar {
				bars := validPatternBars()
				setBar(bars, 59, 10.6, 10.5, 10.7, 10.4)
				return bars
			},
			want: map[Mode]bool{ModePattern: false, ModePullbackOnly: true, ModeHoldOnly: true},
		},
		{
			name: "undercuts anchor without confirmation",
			bars: func() []market.Bar {
				bars := validPatternBars()
				setBar(bars, 58, 10, 9.5, 10, 9.3)
				setBar(bars, 59, 10, 10.1, 10.2, 9.9)
				return bars
			},
			want: map[Mode]bool{ModePattern: false, ModePullbackOnly: true, ModeHoldOnly: false},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, mode := range []Mode{ModePattern, ModePullbackOnly, ModeHoldOnly} {
				bars := tc.bars()
				signals, err := Scan(testHistory(bars), testOptions(bars), mode)
				if err != nil {
					t.Fatal(err)
				}
				if tc.want[mode] {
					if len(signals) != 1 || signals[0].Date != bars[59].Date {
						t.Fatalf("mode %s signals = %+v, want one at %s", mode, signals, bars[59].Date)
					}
					continue
				}
				if len(signals) != 0 {
					t.Fatalf("mode %s signals = %+v, want none", mode, signals)
				}
			}
		})
	}
}

func TestScanControlsUseFirstEligiblePullbackDay(t *testing.T) {
	bars := flatBars(61)
	setBar(bars, 56, 10, 11.5, 13, 9.8)
	setBar(bars, 57, 10.5, 10.4, 10.8, 10.1)
	setBar(bars, 58, 10.3, 10.6, 10.8, 10.2)
	setBar(bars, 59, 10.8, 10.7, 11, 10.6)
	setBar(bars, 60, 11, 11.9, 12.2, 10.9)

	for _, mode := range []Mode{ModePullbackOnly, ModeHoldOnly} {
		signals, err := Scan(testHistory(bars), testOptions(bars), mode)
		if err != nil {
			t.Fatal(err)
		}
		if len(signals) != 1 || signals[0].Date != bars[59].Date {
			t.Fatalf("mode %s signals = %+v, want first pullback day %s", mode, signals, bars[59].Date)
		}
	}

	signals, err := Scan(testHistory(bars), testOptions(bars), ModePattern)
	if err != nil {
		t.Fatal(err)
	}
	if len(signals) != 1 || signals[0].Date != bars[60].Date {
		t.Fatalf("pattern signals = %+v, want confirmation day %s", signals, bars[60].Date)
	}
}

func TestScanRejectsMismatchedAsOfAndUnknownMode(t *testing.T) {
	bars := validPatternBars()
	opts := testOptions(bars)
	opts.AsOf = "2026-10-15"
	if _, err := Scan(testHistory(bars), opts, ModePattern); err == nil {
		t.Fatal("mismatched as-of was accepted")
	}
	if _, err := Scan(testHistory(bars), testOptions(bars), Mode("unknown")); err == nil {
		t.Fatal("unknown mode was accepted")
	}
}

func validPatternBars() []market.Bar {
	bars := flatBars(60)
	setBar(bars, 56, 10, 11.5, 12, 9.8)
	setBar(bars, 57, 10.5, 10.3, 10.8, 10)
	setBar(bars, 58, 10.2, 10.4, 10.9, 10.1)
	setBar(bars, 59, 10.6, 11.1, 11.2, 10.5)
	return bars
}

func setBar(bars []market.Bar, index int, open, close, high, low float64) {
	bars[index] = market.Bar{
		Date: bars[index].Date, Open: open, Close: close,
		High: high, Low: low, Volume: 1000,
	}
}

func flatBars(count int) []market.Bar {
	dates := testWeekdays(count)
	bars := make([]market.Bar, count)
	for i, date := range dates {
		bars[i] = market.Bar{Date: date, Open: 10, Close: 10, High: 10.2, Low: 9.8, Volume: 1000}
	}
	return bars
}

func testHistory(bars []market.Bar) market.History {
	return market.History{Code: "sz000001", AsOf: bars[len(bars)-1].Date, Bars: bars}
}

func testOptions(bars []market.Bar) Options {
	opts := DefaultOptions()
	opts.AsOf = bars[len(bars)-1].Date
	opts.RequirePhase = false
	return opts
}

func testWeekdays(count int) []string {
	day := time.Date(2026, 10, 16, 0, 0, 0, 0, market.Shanghai)
	dates := make([]string, 0, count)
	for len(dates) < count {
		if day.Weekday() != time.Saturday && day.Weekday() != time.Sunday {
			dates = append(dates, day.Format("2006-01-02"))
		}
		day = day.AddDate(0, 0, -1)
	}
	for i, j := 0, len(dates)-1; i < j; i, j = i+1, j-1 {
		dates[i], dates[j] = dates[j], dates[i]
	}
	return dates
}

func appendFutureBars(bars []market.Bar, count int) []market.Bar {
	out := append([]market.Bar(nil), bars...)
	last, _ := time.ParseInLocation("2006-01-02", out[len(out)-1].Date, market.Shanghai)
	for added := 0; added < count; {
		last = last.AddDate(0, 0, 1)
		if last.Weekday() == time.Saturday || last.Weekday() == time.Sunday {
			continue
		}
		out = append(out, market.Bar{
			Date: last.Format("2006-01-02"), Open: 50, Close: 60,
			High: 70, Low: 40, Volume: 1000,
		})
		added++
	}
	return out
}
