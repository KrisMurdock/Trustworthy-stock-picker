package articlechart

import (
	"bytes"
	"image/png"
	"math"
	"strings"
	"testing"
	"time"
)

func TestRenderPNGProducesDeterministicBoundedImage(t *testing.T) {
	spec := testSpec()
	first, err := RenderPNG(spec)
	if err != nil {
		t.Fatal(err)
	}
	second, err := RenderPNG(spec)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("chart rendering is not deterministic")
	}
	if len(first) > MaxPNGBytes {
		t.Fatalf("PNG size = %d", len(first))
	}
	decoded, err := png.Decode(bytes.NewReader(first))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Bounds().Dx() != canvasWidth || decoded.Bounds().Dy() != canvasHeight {
		t.Fatalf("dimensions = %v", decoded.Bounds())
	}
}

func TestSpecValidationRejectsUnsafeInput(t *testing.T) {
	valid := testSpec()
	tests := []struct {
		name   string
		mutate func(*Spec)
	}{
		{name: "id", mutate: func(s *Spec) { s.ID = "../chart" }},
		{name: "as of", mutate: func(s *Spec) { s.AsOf = "not-a-date" }},
		{name: "bar order", mutate: func(s *Spec) { s.Bars[1].Date = s.Bars[0].Date }},
		{name: "ohlc", mutate: func(s *Spec) { s.Bars[0].High = s.Bars[0].Low - 1 }},
		{name: "zone", mutate: func(s *Spec) { s.Zone.Upper = s.Zone.Lower - 1 }},
		{name: "level", mutate: func(s *Spec) { s.Levels[0].Price = 0 }},
		{name: "nan", mutate: func(s *Spec) { s.RetracementPct = math.NaN() }},
		{name: "breakdown not normalized", mutate: func(s *Spec) { s.RetracementExceeded = true }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec := valid
			spec.Bars = append([]Bar(nil), valid.Bars...)
			spec.Levels = append([]Level(nil), valid.Levels...)
			zone := *valid.Zone
			spec.Zone = &zone
			test.mutate(&spec)
			if err := spec.Validate(); err == nil {
				t.Fatal("invalid chart was accepted")
			}
		})
	}
}

func TestEvidenceLinesExposeChanAndWaveDetails(t *testing.T) {
	spec := testSpec()
	chanLine, waveLine := evidenceLines(spec)
	for _, want := range []string{"2 FRACTALS", "2 DISPLAY STROKES", "5 COMPLETED", "ZONE 15.10-15.60"} {
		if !strings.Contains(chanLine, want) {
			t.Fatalf("Chan evidence missing %q: %s", want, chanLine)
		}
	}
	for _, want := range []string{"STAGE PULLBACK", "RETRACE 38.2%", "SUP 15.10", "RES 16.20", "INV 14.90"} {
		if !strings.Contains(waveLine, want) {
			t.Fatalf("wave evidence missing %q: %s", want, waveLine)
		}
	}

	spec.Zone = nil
	spec.Levels = nil
	spec.RetracementPct = 100
	spec.RetracementExceeded = true
	chanLine, waveLine = evidenceLines(spec)
	if !strings.Contains(chanLine, "ZONE N/A") || !strings.Contains(waveLine, "RETRACE >100% BREAKDOWN") || !strings.Contains(waveLine, "LEVELS N/A") {
		t.Fatalf("missing evidence fallback: chan=%s wave=%s", chanLine, waveLine)
	}
}

func TestLegacyDeepRetracementRendersAsBreakdown(t *testing.T) {
	spec := testSpec()
	spec.RetracementPct = 150
	spec.RetracementExceeded = false
	if err := spec.Validate(); err != nil {
		t.Fatalf("legacy chart spec was rejected: %v", err)
	}
	_, waveLine := evidenceLines(spec)
	if !strings.Contains(waveLine, "RETRACE >100% BREAKDOWN") || strings.Contains(waveLine, "150.0%") {
		t.Fatalf("legacy deep retracement was not normalized in evidence: %s", waveLine)
	}
	if _, err := RenderPNG(spec); err != nil {
		t.Fatalf("legacy deep retracement did not render: %v", err)
	}
}

func TestEvidenceLinesFitChartWidth(t *testing.T) {
	fonts, err := newRenderFonts()
	if err != nil {
		t.Fatal(err)
	}
	defer fonts.close()
	chanLine, waveLine := evidenceLines(testSpec())
	if width := textWidth(fonts.subtitle, chanLine); width > canvasWidth-76 {
		t.Fatalf("Chan evidence is too wide: %.1f", width)
	}
	if width := textWidth(fonts.label, waveLine); width > canvasWidth-76 {
		t.Fatalf("wave evidence is too wide: %.1f", width)
	}
}

func testSpec() Spec {
	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	bars := make([]Bar, 0, 60)
	for index := 0; index < 60; index++ {
		price := 15 + 0.03*float64(index) + math.Sin(float64(index)/4)*0.4
		bars = append(bars, Bar{
			Date: start.AddDate(0, 0, index).Format("2006-01-02"),
			Open: price - 0.05, High: price + 0.18, Low: price - 0.18, Close: price,
		})
	}
	return Spec{
		Version: Version, ID: "chart-sz000001", Code: "sz000001",
		AsOf: bars[len(bars)-1].Date, Adjust: "qfq",
		Bars:     bars,
		Strokes:  []Stroke{{FromDate: bars[10].Date, ToDate: bars[28].Date, From: 14.8, To: 16.2, Finished: true}, {FromDate: bars[28].Date, ToDate: bars[len(bars)-1].Date, From: 16.2, To: 16.8}},
		Fractals: []Point{{Date: bars[10].Date, Price: 14.8, Mark: "bottom"}, {Date: bars[28].Date, Price: 16.2, Mark: "top"}},
		Zone:     &Zone{FromDate: bars[16].Date, ToDate: bars[36].Date, Upper: 15.6, Lower: 15.1},
		Levels:   []Level{{Kind: "support", Label: "SUP", Price: 15.1}, {Kind: "resistance", Label: "RES", Price: 16.2}, {Kind: "invalidation", Label: "INV", Price: 14.9}},
		Stage:    "pullback", RetracementPct: 38.2, CompletedStrokes: 5,
	}
}
