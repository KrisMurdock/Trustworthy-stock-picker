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

func TestFinishedStrokeTailAndZones(t *testing.T) {
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
	finished := append([]Stroke{}, strokes...)
	finished[0].Finished = true
	if len(bars)-finished[1].End.Start >= 5 {
		t.Fatal("fixture should leave less than five merged bars after the last stroke")
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
