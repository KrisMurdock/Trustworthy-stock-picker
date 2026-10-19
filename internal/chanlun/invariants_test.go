package chanlun_test

import (
	"reflect"
	"testing"
	"time"

	"stock-watcher/internal/chanlun"
)

var invariantStart = time.Date(2026, 9, 1, 9, 30, 0, 0, time.UTC)

func invariantBar(index int, high, low float64) chanlun.Bar {
	return chanlun.Bar{
		Time:    invariantStart.Add(time.Duration(index) * time.Minute),
		Open:    low,
		High:    high,
		Low:     low,
		Close:   high,
		Volume:  100,
		RawBars: 1,
	}
}

func invariantPrices(prices []float64) []chanlun.Bar {
	bars := make([]chanlun.Bar, len(prices))
	for i, price := range prices {
		bars[i] = invariantBar(i, price+0.5, price-0.5)
	}
	return bars
}

func analyzeInvariant(t *testing.T, bars []chanlun.Bar) chanlun.Analysis {
	t.Helper()
	analysis, err := chanlun.Analyze(bars, 0)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	return analysis
}

func TestInvalidFirstStrokeCandidateDoesNotAbortScan(t *testing.T) {
	// The first bottom-to-top candidate spans only five merged bars. A later
	// legal stroke exists, so one bad candidate must not end the scan.
	analysis := analyzeInvariant(t, invariantPrices([]float64{
		10, 8, 10, 12, 10, 8, 7, 8, 7, 8, 10, 12, 10,
	}))

	if len(analysis.Strokes) == 0 {
		t.Fatal("no strokes built after an invalid first candidate; later candidates must be considered")
	}
	if len(analysis.FinishedStrokes) == 0 {
		t.Fatal("no finished strokes built; a later legal stroke should be confirmed by the following reverse stroke")
	}
}

func TestSameDirectionCandidatesUseDeterministicExtremeEndpoint(t *testing.T) {
	// Two top candidates are separated by a bottom that does not break the
	// starting low. The higher top must be selected and the result must be
	// deterministic for identical input.
	bars := invariantPrices([]float64{
		10, 8, 10, 12, 10, 8, 10, 14, 12, 10, 8, 10, 12,
	})
	first := analyzeInvariant(t, bars)
	second := analyzeInvariant(t, bars)

	if !reflect.DeepEqual(first, second) {
		t.Fatal("Analyze is not deterministic for identical fractal candidates")
	}
	if len(first.FinishedStrokes) == 0 {
		t.Fatal("expected a finished first stroke")
	}
	if got := first.FinishedStrokes[0].End.Price; got != 14.5 {
		t.Fatalf("first stroke endpoint price = %v, want the higher top 14.5", got)
	}
}

func TestEqualHighInclusionIsMergedAndReproducible(t *testing.T) {
	// The second and third bars have equal highs and the third is contained in
	// the second. The equal high must not make the inclusion disappear.
	bars := []chanlun.Bar{
		invariantBar(0, 10, 5),
		invariantBar(1, 10, 6),
		invariantBar(2, 9, 7),
	}
	first := analyzeInvariant(t, bars)
	second := analyzeInvariant(t, bars)

	if !reflect.DeepEqual(first, second) {
		t.Fatal("Analyze is not deterministic for equal-high inclusion input")
	}
	if got := len(first.MergedBars); got != 2 {
		t.Fatalf("merged bars = %d, want 2 after equal-high inclusion", got)
	}
	if first.MergedBars[1].RawBars != 2 {
		t.Fatalf("merged RawBars = %d, want 2", first.MergedBars[1].RawBars)
	}
}

func TestEqualLowInclusionIsMerged(t *testing.T) {
	bars := []chanlun.Bar{
		invariantBar(0, 10, 5),
		invariantBar(1, 10, 6),
		invariantBar(2, 9, 6),
	}
	analysis := analyzeInvariant(t, bars)

	if got := len(analysis.MergedBars); got != 2 {
		t.Fatalf("merged bars = %d, want 2 after equal-low inclusion", got)
	}
	if analysis.MergedBars[1].RawBars != 2 {
		t.Fatalf("merged RawBars = %d, want 2", analysis.MergedBars[1].RawBars)
	}
}

func TestLastStrokeStaysProvisionalWithoutNextReverseStroke(t *testing.T) {
	analysis := analyzeInvariant(t, invariantPrices([]float64{
		10, 8, 10, 11, 12, 10, 8, 7, 8, 9, 10, 11, 12,
	}))

	if len(analysis.Strokes) != 2 {
		t.Fatalf("strokes = %d, want 2", len(analysis.Strokes))
	}
	if analysis.Strokes[1].Finished {
		t.Fatal("last stroke must remain provisional until a later reverse stroke confirms it")
	}
	if len(analysis.FinishedStrokes) != 1 {
		t.Fatalf("finished strokes = %d, want only the confirmed first stroke", len(analysis.FinishedStrokes))
	}
}

func TestFutureBarsDoNotRewriteFinishedPrefixStrokes(t *testing.T) {
	prefix := invariantPrices([]float64{
		10, 8, 10, 11, 12, 10, 8, 7, 8, 9, 10, 11, 12,
	})
	full := invariantPrices([]float64{
		10, 8, 10, 11, 12, 10, 8, 7, 8, 9, 10, 11, 12,
		13, 14, 20, 18, 16, 14,
	})
	before := analyzeInvariant(t, prefix)
	after := analyzeInvariant(t, full)

	if len(after.FinishedStrokes) < len(before.FinishedStrokes) {
		t.Fatalf("finished strokes shrank after future bars: before=%+v after=%+v", before.FinishedStrokes, after.FinishedStrokes)
	}
	for i, stroke := range before.FinishedStrokes {
		if !reflect.DeepEqual(after.FinishedStrokes[i], stroke) {
			t.Fatalf("finished prefix stroke %d changed after future bars: before=%+v after=%+v", i, stroke, after.FinishedStrokes[i])
		}
	}
}

func TestZoneRequiresCommonFinishedStrokeIntersection(t *testing.T) {
	// The first three finished strokes move from a top to a bottom to a much
	// higher top and then down above the first range. They have no common
	// price intersection, so no zone may be published.
	analysis := analyzeInvariant(t, invariantPrices([]float64{
		18, 20, 18, 16, 14, 13, 12, 14, 16, 18, 20, 22, 25,
		24, 23, 22, 23, 24, 25, 24, 22, 23,
	}))

	if len(analysis.FinishedStrokes) < 3 {
		t.Fatalf("fixture must produce at least three finished strokes, got %d", len(analysis.FinishedStrokes))
	}
	if len(analysis.Zones) != 0 {
		t.Fatalf("published %d zone(s) without a common intersection: %+v", len(analysis.Zones), analysis.Zones)
	}
}

func TestZoneDoesNotExtendWithStrokeCompletelyOutside(t *testing.T) {
	analysis := analyzeInvariant(t, invariantPrices([]float64{
		18, 15, 16, 15, 15, 17, 18, 19, 16, 13, 13, 16, 17, 17, 14, 15, 15,
		13, 13, 10, 9, 8, 11, 14, 13, 10, 8, 11, 12, 13, 11, 10, 9, 12,
	}))

	if len(analysis.FinishedStrokes) < 4 {
		t.Fatalf("fixture must produce four finished strokes, got %d", len(analysis.FinishedStrokes))
	}
	zoneStart := invariantStart.Add(time.Minute)
	found := false
	for _, zone := range analysis.Zones {
		if zone.From.Equal(zoneStart) && zone.Strokes == 3 {
			found = true
		}
		if zone.From.Equal(zoneStart) && zone.Strokes > 3 {
			t.Fatalf("zone extended by a stroke completely outside: %+v", zone)
		}
	}
	if !found {
		t.Fatalf("expected a three-stroke zone at %s, got %+v", zoneStart, analysis.Zones)
	}
}
