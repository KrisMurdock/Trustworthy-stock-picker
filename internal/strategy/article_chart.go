package strategy

import (
	"math"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/articlechart"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/chanlun"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

const (
	articleChartDisplayBars = 80
	articleChartAnalysisBar = 240
)

// BuildArticleChart freezes a recent adjusted-price window together with the
// deterministic Chan strokes, overlap zone and wave levels. It returns nil
// when the history cannot be converted into a safe chart payload.
func BuildArticleChart(history market.History, wave *WaveEvidence, fallbackZone *ChanZoneEvidence) *articlechart.Spec {
	if len(history.Bars) < 2 || history.Code == "" {
		return nil
	}
	convertStart := max(0, len(history.Bars)-articleChartAnalysisBar)
	converted := make([]chanlun.Bar, 0, len(history.Bars)-convertStart)
	for _, bar := range history.Bars[convertStart:] {
		day, err := time.ParseInLocation("2006-01-02", bar.Date, market.Shanghai)
		if err != nil || !validWaveBar(bar) {
			return nil
		}
		converted = append(converted, chanlun.Bar{Time: day, Open: bar.Open, High: bar.High, Low: bar.Low, Close: bar.Close, Volume: bar.Volume, RawBars: 1})
	}
	analysis, err := chanlun.Analyze(converted, chanlun.DefaultMinStrokeBars)
	if err != nil {
		return nil
	}
	displayStart := max(0, len(history.Bars)-articleChartDisplayBars)
	display := history.Bars[displayStart:]
	spec := &articlechart.Spec{
		Version:          articlechart.Version,
		ID:               "chart-" + history.Code,
		Code:             history.Code,
		AsOf:             display[len(display)-1].Date,
		Adjust:           history.Adjust,
		Bars:             make([]articlechart.Bar, 0, len(display)),
		Strokes:          []articlechart.Stroke{},
		Fractals:         []articlechart.Point{},
		Levels:           []articlechart.Level{},
		CompletedStrokes: len(analysis.FinishedStrokes),
	}
	firstDate, lastDate := display[0].Date, display[len(display)-1].Date
	for _, bar := range display {
		spec.Bars = append(spec.Bars, articlechart.Bar{Date: bar.Date, Open: bar.Open, High: bar.High, Low: bar.Low, Close: bar.Close})
	}
	for _, stroke := range analysis.Strokes {
		fromDate := stroke.Start.Time.In(market.Shanghai).Format("2006-01-02")
		toDate := stroke.End.Time.In(market.Shanghai).Format("2006-01-02")
		if toDate < firstDate || fromDate > lastDate {
			continue
		}
		spec.Strokes = append(spec.Strokes, articlechart.Stroke{
			FromDate: fromDate, ToDate: toDate,
			From: stroke.Start.Price, To: stroke.End.Price,
			Direction: string(stroke.Direction), Finished: stroke.Finished,
		})
	}
	for _, fractal := range analysis.Fractals {
		date := fractal.Time.In(market.Shanghai).Format("2006-01-02")
		if date < firstDate || date > lastDate {
			continue
		}
		spec.Fractals = append(spec.Fractals, articlechart.Point{Date: date, Price: fractal.Price, Mark: string(fractal.Mark)})
	}
	if zone := analysis.LatestZone(); zone != nil {
		zoneFrom := zone.From.In(market.Shanghai).Format("2006-01-02")
		zoneTo := zone.To.In(market.Shanghai).Format("2006-01-02")
		if zoneTo >= firstDate {
			if zoneFrom < firstDate {
				zoneFrom = firstDate
			}
			spec.Zone = &articlechart.Zone{FromDate: zoneFrom, ToDate: zoneTo, Upper: zone.Upper, Lower: zone.Lower}
		}
	} else if fallbackZone != nil && fallbackZone.Upper > 0 && fallbackZone.Lower > 0 {
		spec.Zone = &articlechart.Zone{Upper: fallbackZone.Upper, Lower: fallbackZone.Lower}
	}
	if wave != nil {
		spec.Stage = wave.Stage
		spec.RetracementPct = math.Min(100, wave.RetracementPct)
		spec.RetracementExceeded = wave.RetracementExceeded || wave.RetracementPct > 100
		sharedSupport := wave.Support > 0 && wave.Invalidation > 0 && math.Abs(wave.Invalidation-wave.Support) < 0.005
		if wave.Support > 0 {
			label := "SUP"
			if sharedSupport {
				label = "SUP/INV"
			}
			spec.Levels = append(spec.Levels, articlechart.Level{Kind: "support", Label: label, Price: wave.Support})
		}
		if wave.Resistance > 0 {
			spec.Levels = append(spec.Levels, articlechart.Level{Kind: "resistance", Label: "RES", Price: wave.Resistance})
		}
		if wave.Invalidation > 0 && !sharedSupport {
			spec.Levels = append(spec.Levels, articlechart.Level{Kind: "invalidation", Label: "INV", Price: wave.Invalidation})
		}
	}
	if err := spec.Validate(); err != nil {
		return nil
	}
	return spec
}
