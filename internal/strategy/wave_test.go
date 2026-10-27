package strategy

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/chanlun"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

func waveSeries(count int, trend, amplitude float64) []market.Bar {
	bars := make([]market.Bar, 0, count)
	start := time.Date(2026, 1, 5, 0, 0, 0, 0, market.Shanghai)
	for i := 0; i < count; i++ {
		price := 10 + trend*float64(i) + amplitude*math.Sin(2*math.Pi*float64(i)/20)
		bars = append(bars, market.Bar{
			Date: start.AddDate(0, 0, i).Format("2006-01-02"),
			Open: price - .05, Close: price, High: price + .15, Low: price - .15, Volume: 1000,
		})
	}
	return bars
}

func TestAnalyzeWaveStagesAndUnknownEvidence(t *testing.T) {
	advanceBars := waveSeries(160, .03, .4)
	advanceLast := advanceBars[len(advanceBars)-1]
	for i := 1; i <= 8; i++ {
		price := advanceLast.Close + float64(i)*.12
		advanceBars = append(advanceBars, market.Bar{
			Date: time.Date(2026, 1, 5, 0, 0, 0, 0, market.Shanghai).AddDate(0, 0, len(advanceBars)).Format("2006-01-02"),
			Open: price - .05, Close: price, High: price + .15, Low: price - .15, Volume: 1000,
		})
	}
	advance := AnalyzeWave(advanceBars)
	if advance.Status != waveReady || advance.Stage != waveStageAdvance || advance.CompletedStrokes == 0 || advance.LastStroke == "" {
		t.Fatalf("obvious rising swing was not described as advance: %+v", advance)
	}
	if advance.RetracementPct < 0 || advance.Evidence == "" || advance.Limitation != waveLimitation {
		t.Fatalf("advance evidence is incomplete: %+v", advance)
	}
	if again := AnalyzeWave(advanceBars); again != advance {
		t.Fatalf("wave evidence is not deterministic: first=%+v second=%+v", advance, again)
	}

	pullbackBars := waveSeries(140, .03, .4)
	last := pullbackBars[len(pullbackBars)-1]
	for i := 1; i <= 12; i++ {
		price := last.Close - float64(i)*.035
		pullbackBars = append(pullbackBars, market.Bar{
			Date: time.Date(2026, 1, 5, 0, 0, 0, 0, market.Shanghai).AddDate(0, 0, len(pullbackBars)).Format("2006-01-02"),
			Open: price + .03, Close: price, High: price + .1, Low: price - .15, Volume: 900,
		})
	}
	pullback := AnalyzeWave(pullbackBars)
	if pullback.Status != waveReady || pullback.Stage != waveStagePullback || pullback.RetracementPct < 10 || pullback.RetracementPct > 61.8 || pullback.CompletedStrokes == 0 {
		t.Fatalf("obvious retracement was not described as a pullback: %+v", pullback)
	}
	if pullback.Support > 0 && pullback.Resistance > 0 && pullback.Support >= pullback.Resistance {
		t.Fatalf("inverted structural levels: %+v", pullback)
	}
	if !strings.Contains(pullback.Evidence, "回撤比例") {
		t.Fatalf("evidence does not describe the normalized retracement: %+v", pullback)
	}

	unknown := AnalyzeWave(waveSeries(120, 0, 0))
	if unknown.Status != waveUnknown || unknown.Stage != waveStageUnknown || unknown.Label != "数据不足" || unknown.CompletedStrokes != 0 {
		t.Fatalf("flat data produced a fabricated swing: %+v", unknown)
	}
	short := AnalyzeWave(waveSeries(waveMinBars-1, .03, .4))
	if short.Status != waveUnknown || short.Stage != waveStageUnknown || short.Evidence == "" {
		t.Fatalf("short history produced a fabricated swing: %+v", short)
	}
}

func TestWaveRetracementUsesImpulseRangeAndMarksBreakdown(t *testing.T) {
	cases := []struct {
		name                   string
		high, low, close, want float64
		wantExceeded           bool
	}{
		{name: "half of impulse", high: 20, low: 10, close: 15, want: 50},
		{name: "close above high", high: 20, low: 10, close: 21, want: 0},
		{name: "invalid range", high: 10, low: 10, close: 9, want: 0},
		{name: "breakdown below impulse start", high: 20, low: 10, close: 5, want: 100, wantExceeded: true},
	}
	for _, tc := range cases {
		got, exceeded := waveRetracement(tc.high, tc.low, tc.close)
		if got != tc.want || exceeded != tc.wantExceeded {
			t.Fatalf("%s: retracement = %v exceeded=%v, want %v exceeded=%v", tc.name, got, exceeded, tc.want, tc.wantExceeded)
		}
	}
}

func TestWaveStructureLevelsUseNearestBoundedCandidates(t *testing.T) {
	zone := &chanlun.Zone{Lower: 31, Upper: 33}
	top := &chanlun.Fractal{High: 33.8, Low: 33.2}
	bottom := &chanlun.Fractal{High: 30.8, Low: 30.5}
	tests := []struct {
		name                                     string
		close, support, resistance, invalidation float64
	}{
		{name: "inside zone", close: 32, support: 31, resistance: 33, invalidation: 30.5},
		{name: "above zone uses nearest upper boundary", close: 33.5, support: 33, resistance: 33.8, invalidation: 30.5},
		{name: "above all candidates omits resistance", close: 35, support: 33, resistance: 0, invalidation: 30.5},
		{name: "below zone omits support and uses lower boundary as resistance", close: 29, support: 0, resistance: 31, invalidation: 30.5},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			support, resistance, invalidation := waveStructureLevels(zone, top, bottom, tc.close)
			if support != tc.support || resistance != tc.resistance || invalidation != tc.invalidation {
				t.Fatalf("levels = support %v resistance %v invalidation %v; want %v %v %v", support, resistance, invalidation, tc.support, tc.resistance, tc.invalidation)
			}
			if support > 0 && resistance > 0 && support >= resistance {
				t.Fatalf("support %v must be below resistance %v", support, resistance)
			}
		})
	}
}

func TestClassifyWaveStageTreatsFullRetracementAsWeak(t *testing.T) {
	stage, label := classifyWaveStage("down", true, 100, 2, 19.35, 20.84, .8, nil)
	if stage != waveStageWeak || !strings.Contains(label, "上行笔起点") {
		t.Fatalf("full retracement stage = %s %s", stage, label)
	}
}

func TestFinalCandidateIncludesWaveAndLatestSignalEvidence(t *testing.T) {
	c, histories, raw := researchFixture()
	result, err := Analyze(context.Background(), c, histories, raw, histories[0].AsOf)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Ranking) != 1 {
		t.Fatalf("unexpected ranking: %+v", result.Ranking)
	}
	candidate := result.Ranking[0]
	if candidate.Wave == nil || candidate.Wave.Version != WaveVersion || candidate.Wave.Method != WaveMethod || candidate.Wave.Limitation != waveLimitation {
		t.Fatalf("final candidate lost wave evidence: %+v", candidate.Wave)
	}
	if candidate.Features.SignalIndicators == nil || candidate.Features.SignalIndicators.Version != 1 {
		t.Fatalf("latest candidate lost signal indicators: %+v", candidate.Features.SignalIndicators)
	}
}
