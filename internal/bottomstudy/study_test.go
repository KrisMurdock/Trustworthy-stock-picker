package bottomstudy

import (
	"context"
	"errors"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"math"
	"reflect"
	"testing"
	"time"
)

func fixture(n int) market.History {
	h := market.History{Version: 1, Code: "sh600036", Name: "样本", Source: "tencent", Adjust: "qfq", DataField: "qfqday", VolumeUnit: "provider_raw", FetchedAt: time.Now()}
	d := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for len(h.Bars) < n {
		if d.Weekday() != time.Saturday && d.Weekday() != time.Sunday {
			h.Bars = append(h.Bars, market.Bar{Date: d.Format(time.DateOnly), Open: 100, Close: 100, High: 101, Low: 99, Volume: 1})
		}
		d = d.AddDate(0, 0, 1)
	}
	h.AsOf = h.Bars[n-1].Date
	return h
}

func events(bars []market.Bar, indices ...int) []Signal {
	s := make([]Signal, len(bars))
	for i, b := range bars {
		s[i] = Signal{Date: b.Date, Close: b.Close}
	}
	for _, i := range indices {
		s[i].Events = []string{"rsi_persistence", "macd_contraction"}
		s[i].States = s[i].Events
	}
	return s
}

func TestEpisodeCountsOnlySignalsKnownBeforeHit(t *testing.T) {
	h := fixture(100)
	h.Bars[59].High = 110 // Signal-day high is unavailable as a future rebound.
	h.Bars[65].High = 104
	s := events(h.Bars, 59, 61, 65, 90)
	e := episodesFor(h.Bars, s, "2026-01-01", "any", 3)
	if len(e) != 3 || e[0].HitDay != 6 || len(e[0].SignalDates) != 2 || e[1].Start != h.Bars[65].Date || e[1].Status != "no_hit" || e[2].Status != "censored" {
		t.Fatalf("episodes: %+v", e)
	}
	summary := summarize(e, "any", 3)
	if summary.Mature != 2 || summary.Hits != 1 || summary.NoHit != 1 || summary.Tail != 1 || summary.HitByCount[2] != 1 {
		t.Fatalf("summary: %+v", summary)
	}
}

func TestTailSuccessDoesNotBiasMatureDenominator(t *testing.T) {
	h := fixture(65)
	h.Bars[61].High = 104
	e := episodesFor(h.Bars, events(h.Bars, 59), "2026-01-01", "any", 3)
	s := summarize(e, "any", 3)
	if s.Mature != 0 || s.Hits != 0 || s.Tail != 1 || s.TailHits != 1 {
		t.Fatal(s)
	}
}

func TestBarrierOrderAndExactBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name            string
		lowDay, highDay int
		order           string
	}{{"target", 3, 2, "target_first"}, {"loss_first", 1, 2, "drawdown_first"}, {"ambiguous", 2, 2, "same_day_unknown"}, {"no_target", 1, 8, "drawdown_first"}} {
		t.Run(tc.name, func(t *testing.T) {
			bars := fixture(10).Bars
			bars[tc.lowDay].Low, bars[tc.highDay].High = 97, 103
			hit, _, order := response(bars, 0, 5, 3)
			if order != tc.order || (tc.highDay <= 5 && hit != tc.highDay) || (tc.highDay > 5 && hit != 0) {
				t.Fatalf("hit=%d order=%s", hit, order)
			}
		})
	}
}

func TestSignalConfirmationAndPersistentState(t *testing.T) {
	b := fixture(100).Bars
	m := make([]metric, len(b))
	for i := range m {
		m[i] = metric{rsi: 50, dif: -3, hist: -4, ma20: 105, lower: 95}
	}
	// Two lower lows, better RSI and DIF. Second pivot cannot be known until 72.
	b[60].Low, b[70].Low = 95, 94
	m[60].rsi, m[70].rsi = 20, 25
	m[60].dif, m[70].dif = -2, -1
	for i := 80; i < 90; i++ {
		m[i].rsi = 20
	}
	for i := 94; i < 99; i++ {
		m[i].rsi = 20
	}
	s := detectMetrics(b, m)
	if has(s[70].Events, "rsi_divergence") || has(s[71].Events, "rsi_divergence") || !has(s[72].Events, "rsi_divergence") || !has(s[72].Events, "macd_divergence") || s[72].Close != 100 || s[72].PivotDate != b[70].Date {
		t.Fatalf("divergence confirmation: %+v", s[70:73])
	}
	for i := 80; i < 100; i++ {
		want := i == 82 || i == 96
		if has(s[i].Events, "rsi_persistence") != want {
			t.Fatalf("persistence should only trigger on entry: %d %+v", i, s[i])
		}
	}
	if !has(s[89].States, "rsi_persistence") {
		t.Fatal("continuous oversold state lost")
	}
}

func TestNearestEligiblePivotAndNoFlatLow(t *testing.T) {
	b := fixture(100).Bars
	m := make([]metric, 100)
	for i := range m {
		m[i] = metric{rsi: 40, dif: -3, hist: -4, ma20: 105, lower: 95}
	}
	b[60].Low, b[70].Low, b[80].Low = 95, 94, 93
	m[60].rsi, m[70].rsi, m[80].rsi = 20, 35, 30
	s := detectMetrics(b, m)
	if has(s[82].Events, "rsi_divergence") {
		t.Fatal("must compare nearest pivot, not favorable older pivot")
	}
	b[81].Low = 93
	s = detectMetrics(b, m)
	if s[82].PivotDate != "" {
		t.Fatal("equal adjacent lows must not form a strict pivot")
	}
}

func TestContractionIsAnEventAndBollingerUsesEachDaysBand(t *testing.T) {
	b := fixture(75).Bars
	m := make([]metric, len(b))
	for i := range m {
		m[i] = metric{rsi: 50, dif: -3, hist: -10, ma20: 105, lower: 95}
	}
	for i := 60; i <= 64; i++ {
		m[i].hist = -9 + float64(i-60)
	}
	// Yesterday's close was below yesterday's band, today's close recovered
	// today's band. Using today's band for both comparisons would miss this.
	m[68].lower, m[69].lower = 101, 99
	s := detectMetrics(b, m)
	for i := 59; i <= 65; i++ {
		if has(s[i].Events, "macd_contraction") != (i == 62) {
			t.Fatalf("incorrect contraction event: %d %+v", i, s[i])
		}
	}
	if !has(s[64].States, "macd_contraction") || !has(s[69].Events, "bollinger_reentry") || has(s[70].Events, "bollinger_reentry") {
		t.Fatal("state or band transition incorrect")
	}
}

func TestLaterSignalReferenceIsNotFirstSignalPrice(t *testing.T) {
	b := fixture(65).Bars
	b[60] = market.Bar{Date: b[60].Date, Open: 95, Close: 95, High: 96, Low: 94}
	b[61] = market.Bar{Date: b[61].Date, Open: 96, Close: 97, High: 98, Low: 95}
	hitFirst, _, _ := response(b, 59, 2, 3)
	hitLater, _, _ := response(b, 60, 1, 3)
	if hitFirst != 0 || hitLater != 1 {
		t.Fatal("later signal must use its own observable close", hitFirst, hitLater)
	}
}

func TestPrefixInvarianceAndFutureCutoff(t *testing.T) {
	h := fixture(160)
	for i := range h.Bars {
		v := 100 + 6*math.Sin(float64(i)/6) - float64(i)/20
		h.Bars[i].Open, h.Bars[i].Close = v, v
		h.Bars[i].Low, h.Bars[i].High = v-1, v+1
	}
	full, err := detect(context.Background(), h.Bars)
	if err != nil {
		t.Fatal(err)
	}
	for _, end := range []int{61, 75, 100, 135} {
		prefix, err := detect(context.Background(), h.Bars[:end])
		if err != nil || !reflect.DeepEqual(prefix, full[:end]) {
			t.Fatalf("future changed signal at prefix %d: %v", end, err)
		}
	}
	options := Options{From: h.Bars[59].Date, Through: h.Bars[129].Date}
	a, err := Analyze(context.Background(), []market.History{h}, options)
	if err != nil {
		t.Fatal(err)
	}
	h.Bars = h.Bars[:130]
	b, err := Analyze(context.Background(), []market.History{h}, options)
	if err != nil || !reflect.DeepEqual(a, b) {
		t.Fatal("bars beyond cutoff changed analysis", err)
	}
	if a.SignalDays == 0 {
		t.Fatal("fixture has no signal coverage")
	}
	for _, c := range a.Conditional {
		if c.Eligible > c.Observed || c.Hits > c.Eligible || c.TargetFirst+c.DrawdownFirst+c.OrderUnknown != c.Hits {
			t.Fatal("conditional denominator/order mismatch", c)
		}
	}
}

func TestInputAndCancellation(t *testing.T) {
	h := fixture(100)
	o := Options{From: h.Bars[59].Date, Through: h.AsOf}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Analyze(ctx, []market.History{h}, o); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	ctx, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	if _, err := Analyze(ctx, []market.History{h}, o); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	for _, histories := range [][]market.History{nil, {h, h}, {fixture(40)}} {
		if _, err := Analyze(context.Background(), histories, o); err == nil {
			t.Fatal("accepted invalid input")
		}
	}
	h.Adjust = "none"
	if _, err := Analyze(context.Background(), []market.History{h}, o); err == nil {
		t.Fatal("accepted unadjusted history")
	}
}
