package predictor

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

func predFeatureTestTime(date string, minute int) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", fmt.Sprintf("%s %02d:%02d", date, minute/60, minute%60), market.Shanghai)
	if err != nil {
		panic(err)
	}
	return t
}

func predFeatureTestEnds(date string) []time.Time {
	out := make([]time.Time, 0, 48)
	for minute := 575; minute <= 690; minute += 5 {
		out = append(out, predFeatureTestTime(date, minute))
	}
	for minute := 785; minute <= 900; minute += 5 {
		out = append(out, predFeatureTestTime(date, minute))
	}
	return out
}

func predFeatureTestData(codes, dates []string) Data {
	data := Data{Minutes: map[string][]market.MinuteBar{}, Daily: map[string][]market.Bar{}}
	for _, code := range codes {
		data.Stocks = append(data.Stocks, config.Stock{Code: code})
		for _, date := range dates {
			bars := make([]market.MinuteBar, 0, 48)
			for i, end := range predFeatureTestEnds(date) {
				close := 10 + float64(i)*0.002
				bars = append(bars, market.MinuteBar{
					End: end, Open: close - 0.004, High: close + 0.012, Low: close - 0.012,
					Close: close, Volume: 1000 + float64(i)*10, Final: true,
				})
			}
			data.Minutes[code] = append(data.Minutes[code], bars...)
		}
		daily := make([]market.Bar, 0, 40)
		for i := 0; i < 40; i++ {
			date := time.Date(2026, 7, 20, 0, 0, 0, 0, market.Shanghai).AddDate(0, 0, i).Format("2006-01-02")
			daily = append(daily, market.Bar{Date: date, Open: 9.5, High: 9.7, Low: 9.4, Close: 9.5 + float64(i)*0.01, Volume: 2000 + float64(i)*20})
		}
		data.Daily[code] = daily
	}
	return data
}

func predFeatureTestClone(data Data) Data {
	out := Data{Stocks: append([]config.Stock(nil), data.Stocks...), Minutes: map[string][]market.MinuteBar{}, Daily: map[string][]market.Bar{}, Market: append([]market.Bar(nil), data.Market...)}
	for code, bars := range data.Minutes {
		out.Minutes[code] = append([]market.MinuteBar(nil), bars...)
	}
	for code, bars := range data.Daily {
		out.Daily[code] = append([]market.Bar(nil), bars...)
	}
	return out
}

func predFeatureTestEqual(a, b float64) bool {
	if math.IsNaN(a) || math.IsNaN(b) {
		return math.IsNaN(a) && math.IsNaN(b)
	}
	return math.Abs(a-b) < 1e-12
}

func predFeatureTestIndex(t *testing.T, names []string, name string) int {
	t.Helper()
	for i, current := range names {
		if current == name {
			return i
		}
	}
	t.Fatalf("feature %q not found", name)
	return -1
}

func TestBuildTrainingSamplesBoundaries(t *testing.T) {
	data := predFeatureTestData([]string{"sh600000"}, []string{"2026-09-01"})
	samples, err := BuildTrainingSamples(data)
	if err != nil {
		t.Fatalf("BuildTrainingSamples: %v", err)
	}
	if len(samples) != 46 {
		t.Fatalf("got %d samples, want 46", len(samples))
	}
	byInput := map[int]Sample{}
	for _, sample := range samples {
		minute := minuteOfDay(sample.InputEnd)
		if minute == 690 || minute == 900 {
			t.Fatalf("input bar %d must be skipped", minute)
		}
		if !sample.TargetEnd.Equal(sample.InputEnd.Add(barStep)) {
			t.Fatalf("target end %s is not input end + 5m", sample.TargetEnd)
		}
		if sample.Date != "2026-09-01" {
			t.Fatalf("unexpected sample date %q", sample.Date)
		}
		if len(sample.Features) != len(FeatureNames()) {
			t.Fatalf("feature count %d, want %d", len(sample.Features), len(FeatureNames()))
		}
		byInput[minute] = sample
	}
	for _, check := range []struct{ input, target int }{{575, 580}, {685, 690}, {785, 790}, {895, 900}} {
		sample, ok := byInput[check.input]
		if !ok {
			t.Fatalf("missing sample for input minute %d", check.input)
		}
		if minuteOfDay(sample.TargetEnd) != check.target {
			t.Fatalf("input %d target %d, want %d", check.input, minuteOfDay(sample.TargetEnd), check.target)
		}
	}
	if _, ok := byInput[690]; ok {
		t.Fatal("11:30 must not be an input sample")
	}
	if _, ok := byInput[900]; ok {
		t.Fatal("15:00 must not be an input sample")
	}
}

func TestBuildTrainingSamplesSkipsNonFinalBars(t *testing.T) {
	data := predFeatureTestData([]string{"sh600000"}, []string{"2026-09-01"})
	bars := data.Minutes["sh600000"]
	for i := range bars {
		if minuteOfDay(bars[i].End) == 600 {
			bars[i].Final = false
		}
	}
	data.Minutes["sh600000"] = bars
	samples, err := BuildTrainingSamples(data)
	if err != nil {
		t.Fatalf("BuildTrainingSamples: %v", err)
	}
	if len(samples) != 44 {
		t.Fatalf("got %d samples, want 44", len(samples))
	}
	for _, sample := range samples {
		if minuteOfDay(sample.InputEnd) == 595 || minuteOfDay(sample.InputEnd) == 600 {
			t.Fatalf("sample with non-final neighbour was not skipped: %s", sample.InputEnd)
		}
	}
}

func TestBuildInferenceFeatureNoFutureLeak(t *testing.T) {
	data := predFeatureTestData([]string{"sh600000", "sz000001"}, []string{"2026-09-01", "2026-09-02"})
	inputEnd := predFeatureTestTime("2026-09-02", 600)
	baseline, err := BuildInferenceFeature(data, "sh600000", inputEnd)
	if err != nil {
		t.Fatalf("BuildInferenceFeature: %v", err)
	}
	if len(baseline.Values) != len(FeatureNames()) {
		t.Fatalf("feature count %d, want %d", len(baseline.Values), len(FeatureNames()))
	}
	mutated := predFeatureTestClone(data)
	for code, bars := range mutated.Minutes {
		for i := range bars {
			if bars[i].End.After(inputEnd) {
				bars[i].Open *= 1.5
				bars[i].High *= 1.5
				bars[i].Low *= 1.5
				bars[i].Close *= 1.5
				bars[i].Volume *= 3
			}
		}
		mutated.Minutes[code] = bars
	}
	for code, bars := range mutated.Daily {
		for i := range bars {
			if bars[i].Date >= "2026-09-02" {
				bars[i].Open *= 2
				bars[i].High *= 2
				bars[i].Low *= 2
				bars[i].Close *= 2
				bars[i].Volume *= 2
			}
		}
		mutated.Daily[code] = bars
	}
	after, err := BuildInferenceFeature(mutated, "sh600000", inputEnd)
	if err != nil {
		t.Fatalf("BuildInferenceFeature after mutation: %v", err)
	}
	for i := range baseline.Values {
		if !predFeatureTestEqual(baseline.Values[i], after.Values[i]) {
			t.Fatalf("feature %s changed after future data mutation: %v -> %v", baseline.Names[i], baseline.Values[i], after.Values[i])
		}
	}
}

func TestBuildTrainingSampleNoFutureLeak(t *testing.T) {
	data := predFeatureTestData([]string{"sh600000"}, []string{"2026-09-01", "2026-09-02"})
	samples, err := BuildTrainingSamples(data)
	if err != nil {
		t.Fatalf("BuildTrainingSamples: %v", err)
	}
	var baseline *Sample
	for i := range samples {
		if samples[i].Date == "2026-09-02" && minuteOfDay(samples[i].InputEnd) == 600 {
			baseline = &samples[i]
			break
		}
	}
	if baseline == nil {
		t.Fatal("missing baseline sample at 10:00")
	}
	mutated := predFeatureTestClone(data)
	bars := mutated.Minutes["sh600000"]
	for i := range bars {
		if bars[i].End.After(baseline.TargetEnd) {
			bars[i].Open *= 1.5
			bars[i].High *= 1.5
			bars[i].Low *= 1.5
			bars[i].Close *= 1.5
			bars[i].Volume *= 3
		}
	}
	mutated.Minutes["sh600000"] = bars
	afterSamples, err := BuildTrainingSamples(mutated)
	if err != nil {
		t.Fatalf("BuildTrainingSamples after mutation: %v", err)
	}
	var after *Sample
	for i := range afterSamples {
		if afterSamples[i].Date == "2026-09-02" && minuteOfDay(afterSamples[i].InputEnd) == 600 {
			after = &afterSamples[i]
			break
		}
	}
	if after == nil {
		t.Fatal("missing sample after mutation")
	}
	for i := range baseline.Features {
		if !predFeatureTestEqual(baseline.Features[i], after.Features[i]) {
			t.Fatalf("feature %d changed after future data mutation: %v -> %v", i, baseline.Features[i], after.Features[i])
		}
	}
	for _, target := range Targets {
		if !predFeatureTestEqual(baseline.Labels[target], after.Labels[target]) {
			t.Fatalf("label %s changed after data beyond the target bar: %v -> %v", target, baseline.Labels[target], after.Labels[target])
		}
	}
}

func TestBuildInferenceRejectsLunchAndClose(t *testing.T) {
	data := predFeatureTestData([]string{"sh600000"}, []string{"2026-09-01"})
	for _, minute := range []int{690, 900} {
		if _, err := BuildInferenceFeature(data, "sh600000", predFeatureTestTime("2026-09-01", minute)); err == nil {
			t.Fatalf("input minute %d must be rejected", minute)
		}
	}
	for _, minute := range []int{685, 895} {
		if _, err := BuildInferenceFeature(data, "sh600000", predFeatureTestTime("2026-09-01", minute)); err != nil {
			t.Fatalf("input minute %d must be accepted: %v", minute, err)
		}
	}
	if _, err := BuildInferenceFeature(data, "sh600000", predFeatureTestTime("2026-09-01", 780)); err == nil {
		t.Fatal("13:00 has no finalized bar and must fail")
	}
}

func TestBuildInferenceReturnsInputBar(t *testing.T) {
	data := predFeatureTestData([]string{"sh600000"}, []string{"2026-09-01"})
	inputEnd := predFeatureTestTime("2026-09-01", 605)
	vector, bar, err := BuildInference(data, "sh600000", inputEnd)
	if err != nil {
		t.Fatalf("BuildInference: %v", err)
	}
	if !bar.End.Equal(inputEnd) || !bar.Final {
		t.Fatalf("unexpected input bar: end=%s final=%v", bar.End, bar.Final)
	}
	if len(vector.Values) != len(FeatureNames()) {
		t.Fatalf("feature count %d, want %d", len(vector.Values), len(FeatureNames()))
	}
	for i := range vector.Names {
		if vector.Names[i] != FeatureNames()[i] {
			t.Fatalf("feature order changed at %d: %s", i, vector.Names[i])
		}
	}
}

func TestFeaturesDoNotCrossLunch(t *testing.T) {
	data := predFeatureTestData([]string{"sh600000"}, []string{"2026-09-01"})
	vector, err := BuildInferenceFeature(data, "sh600000", predFeatureTestTime("2026-09-01", 790))
	if err != nil {
		t.Fatalf("BuildInferenceFeature 13:10: %v", err)
	}
	value := func(name string) float64 { return vector.Values[predFeatureTestIndex(t, vector.Names, name)] }
	if math.IsNaN(value("ret1")) {
		t.Fatal("13:10 ret1 should use the 13:05 bar")
	}
	for _, name := range []string{"ret3", "ret6", "ma5_dist", "vol12"} {
		if !math.IsNaN(value(name)) {
			t.Fatalf("13:10 %s must be missing because the window crosses lunch", name)
		}
	}
	if value("has_24_bars") != 0 {
		t.Fatal("13:10 must not report 24 contiguous bars")
	}

	late, err := BuildInferenceFeature(data, "sh600000", predFeatureTestTime("2026-09-01", 895))
	if err != nil {
		t.Fatalf("BuildInferenceFeature 14:55: %v", err)
	}
	lateValue := func(name string) float64 { return late.Values[predFeatureTestIndex(t, late.Names, name)] }
	if math.IsNaN(lateValue("ret12")) {
		t.Fatal("14:55 ret12 should use only afternoon bars")
	}
	if !math.IsNaN(lateValue("ma24_dist")) || lateValue("has_24_bars") != 0 {
		t.Fatal("14:55 must not fabricate a 24-bar window across lunch")
	}
}

func TestPoolFeaturesUseOnlySameTimestamp(t *testing.T) {
	data := predFeatureTestData([]string{"sh600000", "sz000001"}, []string{"2026-09-01"})
	inputEnd := predFeatureTestTime("2026-09-01", 600)
	baseline, err := BuildInferenceFeature(data, "sh600000", inputEnd)
	if err != nil {
		t.Fatalf("BuildInferenceFeature: %v", err)
	}
	idx := predFeatureTestIndex(t, baseline.Names, "pool_ret1")

	sameTimestamp := predFeatureTestClone(data)
	sameBars := sameTimestamp.Minutes["sz000001"]
	for i := range sameBars {
		if sameBars[i].End.Equal(inputEnd) {
			sameBars[i].Open = sameBars[i].Close * 0.9
			sameBars[i].High = sameBars[i].Close * 1.01
			sameBars[i].Low = sameBars[i].Close * 0.89
		}
	}
	sameTimestamp.Minutes["sz000001"] = sameBars
	changed, err := BuildInferenceFeature(sameTimestamp, "sh600000", inputEnd)
	if err != nil {
		t.Fatalf("BuildInferenceFeature after same-timestamp change: %v", err)
	}
	if predFeatureTestEqual(baseline.Values[idx], changed.Values[idx]) {
		t.Fatal("same-timestamp cross-section change did not affect pool_ret1")
	}

	later := predFeatureTestClone(data)
	laterBars := later.Minutes["sz000001"]
	for i := range laterBars {
		if laterBars[i].End.After(inputEnd) {
			laterBars[i].Close *= 2
		}
	}
	later.Minutes["sz000001"] = laterBars
	unchanged, err := BuildInferenceFeature(later, "sh600000", inputEnd)
	if err != nil {
		t.Fatalf("BuildInferenceFeature after later change: %v", err)
	}
	if !predFeatureTestEqual(baseline.Values[idx], unchanged.Values[idx]) {
		t.Fatal("later cross-section bar leaked into pool_ret1")
	}

	extra := predFeatureTestData([]string{"sh600000", "sz000001", "sz000002"}, []string{"2026-09-01"})
	extra.Stocks = extra.Stocks[:2]
	withoutExtra, err := BuildInferenceFeature(extra, "sh600000", inputEnd)
	if err != nil {
		t.Fatalf("BuildInferenceFeature without extra code: %v", err)
	}
	extraBars := extra.Minutes["sz000002"]
	for i := range extraBars {
		if extraBars[i].End.Equal(inputEnd) {
			extraBars[i].Open = extraBars[i].Close * 0.5
			extraBars[i].Close *= 2
		}
	}
	extra.Minutes["sz000002"] = extraBars
	withExtra, err := BuildInferenceFeature(extra, "sh600000", inputEnd)
	if err != nil {
		t.Fatalf("BuildInferenceFeature with extra code: %v", err)
	}
	if !predFeatureTestEqual(withoutExtra.Values[idx], withExtra.Values[idx]) {
		t.Fatal("code outside the configured stock pool leaked into pool_ret1")
	}
}

func TestTechnicalAndMarketContextFeatures(t *testing.T) {
	data := predFeatureTestData([]string{"sh600000"}, []string{"2026-09-01", "2026-09-02"})
	data.Market = append([]market.Bar(nil), data.Daily["sh600000"]...)
	for i := range data.Market {
		data.Market[i].Close = 100 + float64(i)*0.1
		data.Market[i].Open = data.Market[i].Close
		data.Market[i].High = data.Market[i].Close + 0.2
		data.Market[i].Low = data.Market[i].Close - 0.2
	}
	vector, err := BuildInferenceFeature(data, "sh600000", predFeatureTestTime("2026-09-02", 895))
	if err != nil {
		t.Fatalf("BuildInferenceFeature: %v", err)
	}
	value := func(name string) float64 { return vector.Values[predFeatureTestIndex(t, vector.Names, name)] }
	for _, name := range []string{"macd_line", "macd_hist", "boll_pctb", "boll_width", "day_macd_hist", "day_boll_pctb", "day_boll_width", "mkt_ret1", "mkt_ret5", "mkt_ret20", "mkt_ma20_dist", "mkt_vol20", "rel_ret5", "rel_ret20", "beta60", "down_beta60"} {
		if math.IsNaN(value(name)) {
			t.Fatalf("feature %s is unexpectedly missing", name)
		}
	}
	if value("has_macd") != 1 || value("has_daily_macd") != 1 || value("has_market") != 1 || value("has_beta60") != 1 {
		t.Fatalf("context availability flags = macd %.0f daily %.0f market %.0f beta %.0f", value("has_macd"), value("has_daily_macd"), value("has_market"), value("has_beta60"))
	}
	if beta := value("beta60"); beta < 0.5 || beta > 1.5 {
		t.Fatalf("aligned market beta = %.6f, want a positive near-one exposure", beta)
	}
}

func TestMarketContextFeaturesIgnoreFutureBars(t *testing.T) {
	data := predFeatureTestData([]string{"sh600000"}, []string{"2026-09-01", "2026-09-02"})
	data.Market = append([]market.Bar(nil), data.Daily["sh600000"]...)
	inputEnd := predFeatureTestTime("2026-09-02", 600)
	baseline, err := BuildInferenceFeature(data, "sh600000", inputEnd)
	if err != nil {
		t.Fatalf("BuildInferenceFeature: %v", err)
	}
	mutated := predFeatureTestClone(data)
	for i := range mutated.Market {
		if mutated.Market[i].Date >= "2026-09-02" {
			mutated.Market[i].Close *= 3
			mutated.Market[i].High *= 3
			mutated.Market[i].Low *= 3
			mutated.Market[i].Volume *= 5
		}
	}
	after, err := BuildInferenceFeature(mutated, "sh600000", inputEnd)
	if err != nil {
		t.Fatalf("BuildInferenceFeature after market mutation: %v", err)
	}
	for i := range baseline.Values {
		if !predFeatureTestEqual(baseline.Values[i], after.Values[i]) {
			t.Fatalf("future market data leaked into feature %s: %v -> %v", baseline.Names[i], baseline.Values[i], after.Values[i])
		}
	}
}

func TestMarketContextMissingIsExplicit(t *testing.T) {
	data := predFeatureTestData([]string{"sh600000"}, []string{"2026-09-01"})
	vector, err := BuildInferenceFeature(data, "sh600000", predFeatureTestTime("2026-09-01", 600))
	if err != nil {
		t.Fatalf("BuildInferenceFeature: %v", err)
	}
	value := func(name string) float64 { return vector.Values[predFeatureTestIndex(t, vector.Names, name)] }
	if value("has_market") != 0 || !math.IsNaN(value("mkt_ret1")) || !math.IsNaN(value("beta60")) {
		t.Fatalf("missing market context was not represented explicitly: has_market=%v mkt_ret1=%v beta60=%v", value("has_market"), value("mkt_ret1"), value("beta60"))
	}
}

func TestRollingBetaUsesPrecomputedMarketReturns(t *testing.T) {
	returns := []float64{0.01, -0.02, 0.015, -0.005, 0.004, -0.01, 0.02, -0.015, 0.003, -0.007,
		0.012, -0.006, 0.008, -0.011, 0.005, -0.004, 0.009, -0.013, 0.006, -0.002, 0.007, -0.008, 0.011, -0.003}
	marketBars := make([]market.Bar, 0, len(returns)+1)
	stockBars := make([]market.Bar, 0, len(returns)+1)
	marketClose, stockClose := 100.0, 100.0
	marketBars = append(marketBars, market.Bar{Date: "2026-01-01", Close: marketClose})
	stockBars = append(stockBars, market.Bar{Date: "2026-01-01", Close: stockClose})
	for i, r := range returns {
		date := time.Date(2026, 1, 2+i, 0, 0, 0, 0, market.Shanghai).Format("2006-01-02")
		marketClose *= 1 + r
		stockClose *= 1 + 2*r
		marketBars = append(marketBars, market.Bar{Date: date, Close: marketClose})
		stockBars = append(stockBars, market.Bar{Date: date, Close: stockClose})
	}
	beta, downBeta, ok := rollingBeta(stockBars, len(stockBars)-1, returnMap(marketBars), 60)
	if !ok || math.Abs(beta-2) > 1e-9 || math.Abs(downBeta-2) > 1e-9 {
		t.Fatalf("rolling beta = %.9f down %.9f ok=%v, want 2/2", beta, downBeta, ok)
	}
}

func BenchmarkBuildTrainingSamples(b *testing.B) {
	codes := make([]string, 20)
	for i := range codes {
		codes[i] = fmt.Sprintf("sh%06d", i+1)
	}
	dates := make([]string, 17)
	for i := range dates {
		dates[i] = time.Date(2026, 9, 1, 0, 0, 0, 0, market.Shanghai).AddDate(0, 0, i).Format("2006-01-02")
	}
	data := predFeatureTestData(codes, dates)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := BuildTrainingSamples(data); err != nil {
			b.Fatal(err)
		}
	}
}
