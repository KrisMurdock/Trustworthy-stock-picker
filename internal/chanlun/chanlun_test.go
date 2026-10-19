package chanlun

import (
	"math"
	"testing"
	"time"
)

func testBar(index int, high, low float64) Bar {
	return Bar{
		Time: time.Date(2026, 10, 1, 9, 30+index, 0, 0, time.UTC),
		Open: low, High: high, Low: low, Close: high, Volume: 100, RawBars: 1,
	}
}

func TestInclusionMergeUsesPriorDirection(t *testing.T) {
	up, err := Analyze([]Bar{
		testBar(0, 10, 5),
		testBar(1, 12, 7),
		testBar(2, 11, 8),
	}, 3)
	if err != nil || len(up.MergedBars) != 2 {
		t.Fatalf("up inclusion not merged: %+v %v", up.MergedBars, err)
	}
	if last := up.MergedBars[1]; last.High != 12 || last.Low != 8 || last.RawBars != 2 {
		t.Fatalf("bad up merge: %+v", last)
	}

	down, err := Analyze([]Bar{
		testBar(0, 12, 7),
		testBar(1, 10, 5),
		testBar(2, 9, 6),
	}, 3)
	if err != nil || len(down.MergedBars) != 2 {
		t.Fatalf("down inclusion not merged: %+v %v", down.MergedBars, err)
	}
	if last := down.MergedBars[1]; last.High != 9 || last.Low != 5 || last.RawBars != 2 {
		t.Fatalf("bad down merge: %+v", last)
	}
}

func TestInclusionDirectionTieBreaks(t *testing.T) {
	if got := inclusionDirection(Bar{High: 10, Low: 5}, Bar{High: 11, Low: 6}, 0); got != 1 {
		t.Fatalf("higher high should be upward, got %d", got)
	}
	if got := inclusionDirection(Bar{High: 11, Low: 6}, Bar{High: 10, Low: 5}, 0); got != -1 {
		t.Fatalf("lower high should be downward, got %d", got)
	}
	if got := inclusionDirection(Bar{High: 10, Low: 5}, Bar{High: 10, Low: 6}, 0); got != 1 {
		t.Fatalf("higher low should be upward, got %d", got)
	}
	if got := inclusionDirection(Bar{High: 10, Low: 6}, Bar{High: 10, Low: 5}, 0); got != -1 {
		t.Fatalf("lower low should be downward, got %d", got)
	}
	if got := inclusionDirection(Bar{High: 10, Low: 5}, Bar{High: 10, Low: 5}, -1); got != -1 {
		t.Fatalf("equal bars should keep prior direction, got %d", got)
	}
	if got := inclusionDirection(Bar{High: 10, Low: 5}, Bar{High: 10, Low: 5}, 0); got != 1 {
		t.Fatalf("equal bars without prior direction should default upward, got %d", got)
	}
}

func TestInclusionMergeEqualHighDoesNotSkip(t *testing.T) {
	merged := mergeInclusions([]Bar{
		testBar(0, 10, 5),
		testBar(1, 10, 5),
		testBar(2, 9, 5.5),
	})
	if len(merged) != 2 {
		t.Fatalf("equal-high inclusion was not merged: %+v", merged)
	}
	if last := merged[1]; last.High != 10 || last.Low != 5.5 || last.RawBars != 2 {
		t.Fatalf("bad equal-high merge: %+v", last)
	}
}

func TestAppendFractalKeepsSameMarkExtreme(t *testing.T) {
	t0 := testBar(0, 1, 1).Time
	t1 := testBar(1, 1, 1).Time
	earlyTop := Fractal{Mark: Top, Time: t0, High: 10, Low: 7, Price: 10, Index: 1}
	out := appendFractal([]Fractal{earlyTop}, Fractal{Mark: Top, Time: t1, High: 12, Low: 8, Price: 12, Index: 3})
	if len(out) != 1 || out[0].High != 12 {
		t.Fatalf("higher top did not replace earlier top: %+v", out)
	}
	out = appendFractal(out, Fractal{Mark: Top, Time: t1.Add(time.Minute), High: 12, Low: 9, Price: 12, Index: 5})
	if len(out) != 1 || out[0].Low != 8 {
		t.Fatalf("equal top should keep earlier fractal: %+v", out)
	}

	earlyBottom := Fractal{Mark: Bottom, Time: t0, High: 10, Low: 8, Price: 8, Index: 1}
	out = appendFractal([]Fractal{earlyBottom}, Fractal{Mark: Bottom, Time: t1, High: 9, Low: 6, Price: 6, Index: 3})
	if len(out) != 1 || out[0].Low != 6 {
		t.Fatalf("lower bottom did not replace earlier bottom: %+v", out)
	}
	out = appendFractal(out, Fractal{Mark: Bottom, Time: t1.Add(time.Minute), High: 8, Low: 6, Price: 6, Index: 5})
	if len(out) != 1 || out[0].High != 9 {
		t.Fatalf("equal bottom should keep earlier fractal: %+v", out)
	}
}

func TestStrictFractalsAndMinimumStrokeLength(t *testing.T) {
	bars := []Bar{
		testBar(0, 10, 5),
		testBar(1, 13, 8),
		testBar(2, 11, 6),
		testBar(3, 9, 4),
		testBar(4, 12, 7),
		testBar(5, 14, 9),
		testBar(6, 12, 7),
		testBar(7, 9, 3),
		testBar(8, 11, 6),
		testBar(9, 10, 5),
	}
	fractals := detectFractals(bars)
	if len(fractals) != 5 || fractals[0].Mark != Top || fractals[1].Mark != Bottom || fractals[2].Mark != Top || fractals[3].Mark != Bottom || fractals[4].Mark != Top {
		t.Fatalf("strict fractals not detected: %+v", fractals)
	}
	shortBars := bars[:5]
	shortFractals := detectFractals(shortBars)
	if got := buildStrokes(shortFractals, shortBars, 6); len(got) != 0 {
		t.Fatalf("short strokes accepted: %+v", got)
	}
	if got := buildStrokes(shortFractals, shortBars, 3); len(got) != 1 || got[0].Direction != Down {
		t.Fatalf("minimum-length stroke rejected: %+v", got)
	}
}

func TestBuildStrokesContinuesAfterInvalidFirstStroke(t *testing.T) {
	bars := make([]Bar, 8)
	for i := range bars {
		bars[i] = testBar(i, 10, 5)
	}
	fractals := []Fractal{
		{Mark: Bottom, Index: 1, Start: 0, End: 2, High: 10, Low: 8, Price: 8},
		{Mark: Top, Index: 3, Start: 2, End: 4, High: 9.5, Low: 9, Price: 9.5},
		{Mark: Bottom, Index: 6, Start: 5, End: 7, High: 8, Low: 7, Price: 7},
	}
	got := buildStrokes(fractals, bars, 6)
	if len(got) != 1 || got[0].Direction != Down || got[0].Start.Mark != Top {
		t.Fatalf("valid stroke after short candidate was lost: %+v", got)
	}
}

func TestBuildStrokesContinuesAfterContainedFirstStroke(t *testing.T) {
	bars := make([]Bar, 15)
	for i := range bars {
		bars[i] = testBar(i, 10, 5)
	}
	fractals := []Fractal{
		{Mark: Bottom, Index: 1, Start: 0, End: 2, High: 10, Low: 8, Price: 8},
		{Mark: Top, Index: 10, Start: 9, End: 11, High: 9.5, Low: 9, Price: 9.5},
		{Mark: Bottom, Index: 14, Start: 12, End: 14, High: 8, Low: 7, Price: 7},
	}
	got := buildStrokes(fractals, bars, 6)
	if len(got) != 1 || got[0].Direction != Down || got[0].Start.Mark != Top {
		t.Fatalf("valid stroke after contained candidate was lost: %+v", got)
	}
}

func TestBuildZonesUsesFinishedStrokeOverlap(t *testing.T) {
	fractals := []Fractal{
		{Mark: Bottom, Index: 1, Start: 0, End: 2, Time: testBar(1, 1, 1).Time, High: 10, Low: 8, Price: 8},
		{Mark: Top, Index: 5, Start: 4, End: 6, Time: testBar(5, 1, 1).Time, High: 12, Low: 10, Price: 12},
		{Mark: Bottom, Index: 9, Start: 8, End: 10, Time: testBar(9, 1, 1).Time, High: 11, Low: 9, Price: 9},
	}
	bars := make([]Bar, 12)
	for i := range bars {
		bars[i] = testBar(i, 10, 8)
	}
	strokes := buildStrokes(fractals, bars, 6)
	if len(strokes) != 2 || strokes[0].Direction != Up || strokes[1].Direction != Down {
		t.Fatalf("bad strokes: %+v", strokes)
	}
	zones := buildZones([]Stroke{
		{Direction: Up, High: 12, Low: 8, Start: fractals[0], End: fractals[1]},
		{Direction: Down, High: 11, Low: 9, End: fractals[2]},
		{Direction: Up, High: 13, Low: 10, End: fractals[2]},
	})
	if len(zones) != 1 || zones[0].Upper != 11 || zones[0].Lower != 10 || zones[0].Strokes != 3 {
		t.Fatalf("bad zone: %+v", zones)
	}
	separate := buildZones([]Stroke{
		{Direction: Up, High: 12, Low: 8},
		{Direction: Down, High: 11, Low: 9},
		{Direction: Up, High: 13, Low: 10},
		{Direction: Down, High: 17, Low: 14},
	})
	if len(separate) != 1 {
		t.Fatalf("incomplete trailing zone published: %+v", separate)
	}
}

func TestBuildZonesRequiresTwoWayIntersection(t *testing.T) {
	base := []Stroke{
		{Direction: Up, High: 12, Low: 8},
		{Direction: Down, High: 11, Low: 9},
		{Direction: Up, High: 13, Low: 10},
	}
	above := append(append([]Stroke{}, base...), Stroke{Direction: Up, High: 17, Low: 14})
	if zones := buildZones(above); len(zones) != 1 || zones[0].Strokes != 3 {
		t.Fatalf("up stroke fully above zone extended it: %+v", zones)
	}
	below := append(append([]Stroke{}, base...), Stroke{Direction: Down, High: 7, Low: 5})
	if zones := buildZones(below); len(zones) != 1 || zones[0].Strokes != 3 {
		t.Fatalf("down stroke fully below zone extended it: %+v", zones)
	}
	overlap := append(append([]Stroke{}, base...), Stroke{Direction: Down, High: 11.5, Low: 9.5})
	if zones := buildZones(overlap); len(zones) != 1 || zones[0].Strokes != 4 {
		t.Fatalf("overlapping stroke did not extend zone: %+v", zones)
	}
}

func TestAnalyzeLeavesLastStrokeProvisional(t *testing.T) {
	bars := []Bar{
		testBar(0, 9, 7),
		testBar(1, 10, 8.5),
		testBar(2, 9.5, 7.8),
		testBar(3, 9, 7),
		testBar(4, 9.5, 7.8),
		testBar(5, 12, 9),
		testBar(6, 11, 8),
		testBar(7, 8, 7),
		testBar(8, 11, 7.5),
		testBar(9, 14, 10),
		testBar(10, 11, 7.2),
		testBar(11, 8, 6),
		testBar(12, 11, 6.5),
		testBar(13, 16, 12),
		testBar(14, 11, 7),
		testBar(15, 10, 6.5),
		testBar(16, 9, 6),
		testBar(17, 8, 5.5),
		testBar(18, 7, 5),
		testBar(19, 6, 4.5),
	}
	analysis, err := Analyze(bars, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(analysis.Strokes) < 2 {
		t.Fatalf("expected multiple strokes, got %+v", analysis.Strokes)
	}
	last := len(analysis.Strokes) - 1
	if analysis.Strokes[last].Finished {
		t.Fatalf("last stroke should be provisional: %+v", analysis.Strokes)
	}
	for i, stroke := range analysis.Strokes[:last] {
		if !stroke.Finished {
			t.Fatalf("non-last stroke %d should be finished: %+v", i, analysis.Strokes)
		}
	}
	if len(analysis.FinishedStrokes) != last {
		t.Fatalf("provisional stroke leaked into finished strokes: %+v", analysis.FinishedStrokes)
	}
	if len(analysis.MergedBars)-analysis.Strokes[last].End.Start < 5 {
		t.Fatal("fixture must leave enough tail bars to reject the old tail heuristic")
	}
}

func TestAnalyzeRejectsMalformedInput(t *testing.T) {
	bad := testBar(0, 10, 5)
	bad.High = math.NaN()
	if _, err := Analyze([]Bar{bad}, 6); err == nil {
		t.Fatal("NaN accepted")
	}
	first, second := testBar(0, 10, 5), testBar(1, 12, 7)
	second.Time = first.Time
	if _, err := Analyze([]Bar{first, second}, 6); err == nil {
		t.Fatal("duplicate time accepted")
	}
	if _, err := Analyze(nil, 0); err != nil {
		t.Fatal(err)
	}
}
