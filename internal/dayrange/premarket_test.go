package dayrange

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

func weekdayDays(start string, count int) []string {
	day, _ := time.ParseInLocation(time.DateOnly, start, shanghai)
	out := make([]string, 0, count)
	for len(out) < count {
		if day.Weekday() != time.Saturday && day.Weekday() != time.Sunday {
			out = append(out, day.Format(time.DateOnly))
		}
		day = day.AddDate(0, 0, 1)
	}
	return out
}

func premarketData(days []string, codes ...string) MarketData {
	out := MarketData{TradingDays: append([]string(nil), days...), Statuses: map[string]map[string]market.SessionStatus{}}
	for _, code := range codes {
		item := InputSeries{Stock: config.Stock{Code: code, Name: "测试" + code, Sector: "银行"}, RawSource: map[string]string{}, AdjustedSource: map[string]string{}}
		out.Statuses[code] = map[string]market.SessionStatus{}
		for i, day := range days {
			close := 10 + float64(i)*.02
			raw := market.Bar{Date: day, Open: close, Close: close, High: close + .15, Low: close - .15, Volume: 1000 + float64(i)}
			adj := market.Bar{Date: day, Open: close, Close: close, High: close + .01, Low: close - .01, Volume: raw.Volume}
			item.Raw = append(item.Raw, raw)
			item.Adjusted = append(item.Adjusted, adj)
			item.RawSource[day], item.AdjustedSource[day] = "tencent", "tencent"
			out.Statuses[code][day] = market.SessionStatus{Code: code, Date: day, State: market.SessionTraded, Source: "baostock"}
		}
		out.Series = append(out.Series, item)
	}
	return out
}

func TestBuildPredictionSnapshotsUsesRawReferenceAndRawFeatures(t *testing.T) {
	days := weekdayDays("2026-07-01", 70)
	data := premarketData(days, "sz000001")
	cfg := config.DefaultDayRange()
	cfg.Enabled = true
	ctx := context.Background()
	now := time.Date(2026, 10, 21, 8, 45, 0, 0, shanghai)
	first, err := BuildPredictionSnapshots(ctx, cfg, []config.Stock{{Code: "sz000001", Name: "测试", Sector: "银行"}}, data, days[len(days)-1], now)
	if err != nil || len(first) != 1 || first[0].Reason != "" {
		t.Fatalf("snapshot failed: %+v %v", first, err)
	}
	snapshot := first[0].Snapshot
	if !snapshot.AsOf.Equal(now) || !snapshot.CreatedAt.Equal(now) {
		t.Fatalf("snapshot did not retain the actual premarket generation time: %+v", snapshot)
	}
	ref := data.Series[0].Raw[len(data.Series[0].Raw)-2]
	if snapshot.ReferencePrice != ref.Close {
		t.Fatalf("reference price is not the previous raw close: %v != %v", snapshot.ReferencePrice, ref.Close)
	}
	prev := data.Series[0].Raw[len(data.Series[0].Raw)-3]
	wantRange := (prev.High - prev.Low) / prev.Close
	if snapshot.Features[0].Value == nil || *snapshot.Features[0].Value != wantRange {
		t.Fatalf("features did not use raw daily bars: %+v want %v", snapshot.Features[0], wantRange)
	}
	originalID, _ := SnapshotID(snapshot)

	data.Series[0].Raw[len(data.Series[0].Raw)-1] = market.Bar{Date: days[len(days)-1], Open: 99, Close: 100, High: 101, Low: 98, Volume: 1}
	data.Series[0].Adjusted[len(data.Series[0].Adjusted)-1] = market.Bar{Date: days[len(days)-1], Open: 88, Close: 90, High: 91, Low: 87, Volume: 1}
	second, err := BuildPredictionSnapshots(ctx, cfg, []config.Stock{{Code: "sz000001", Name: "测试", Sector: "银行"}}, data, days[len(days)-1], now)
	if err != nil || len(second) != 1 {
		t.Fatal(err)
	}
	changedID, _ := SnapshotID(second[0].Snapshot)
	if originalID != changedID {
		t.Fatal("changing target-day future bars changed the premarket snapshot")
	}
}

func TestSnapshotJSONIsCompactAndRoundTrips(t *testing.T) {
	value := .25
	snapshot := Snapshot{SchemaVersion: SnapshotSchemaVersion, Code: "sz000001", TargetDate: "2026-10-21", Mode: ModePremarket,
		FeatureVersion: FeatureVersion, Features: make([]Feature, len(featureNames))}
	for i, name := range featureNames {
		snapshot.Features[i] = Feature{Name: name}
	}
	snapshot.Features[0].Value = &value
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(featureNames[0])) {
		t.Fatalf("snapshot repeated feature names: %s", raw)
	}
	var restored Snapshot
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	values := restored.Values()
	if len(values) != len(featureNames) || values[0] != value || restored.Features[1].Name != featureNames[1] {
		t.Fatalf("snapshot round-trip lost feature order: %+v", restored.Features)
	}
}

func TestSnapshotReadySupportsIndependentlyDisabledFeatureLayers(t *testing.T) {
	forecast := Forecast{}
	features := make([]Feature, len(featureNames))
	base := Snapshot{ReferencePrice: 10, Features: features, Baseline: &forecast}
	for name, availability := range map[string]Availability{
		"stock":  {Stock: "available", Market: "disabled", Sector: "disabled"},
		"market": {Stock: "disabled", Market: "available", Sector: "disabled"},
		"sector": {Stock: "disabled", Market: "disabled", Sector: "available"},
	} {
		t.Run(name, func(t *testing.T) {
			snapshot := base
			snapshot.Availability = availability
			if !snapshot.Ready() {
				t.Fatalf("snapshot with available %s layer was not ready: %+v", name, snapshot.Availability)
			}
		})
	}
	unavailable := base
	unavailable.Availability = Availability{Stock: "disabled", Market: "insufficient_history", Sector: "missing_sector"}
	if unavailable.Ready() {
		t.Fatal("snapshot without an available feature layer was ready")
	}
	missingBaseline := unavailable
	missingBaseline.Baseline = nil
	missingBaseline.Availability.Stock = "available"
	if missingBaseline.Ready() {
		t.Fatal("snapshot without baseline was ready")
	}
	invalidReference := missingBaseline
	invalidReference.Baseline = &forecast
	invalidReference.ReferencePrice = 0
	if invalidReference.Ready() {
		t.Fatal("snapshot without a valid reference price was ready")
	}
	missingFeatures := missingBaseline
	missingFeatures.Baseline = &forecast
	missingFeatures.Features = features[:len(features)-1]
	if missingFeatures.Ready() {
		t.Fatal("snapshot with an invalid feature count was ready")
	}
}

func TestMarketBreadthAveragesAllRecentTradingDays(t *testing.T) {
	days := weekdayDays("2026-07-01", 25)
	series := make([]InputSeries, 5)
	returns := map[string]map[string]float64{}
	for i := range series {
		code := "sz00000" + string(rune('1'+i))
		series[i] = InputSeries{Stock: config.Stock{Code: code}}
		returns[code] = map[string]float64{}
		for _, day := range days[1:] {
			value := .01
			if i >= 2 {
				value = -.01
			}
			returns[code][day] = value
		}
	}
	stats := buildMarketStats(days, series, returns)
	got := stats[days[21]]
	if !got.available || math.Abs(got.breadth5-.4) > 1e-12 {
		t.Fatalf("breadth did not average all five recent trading days: %+v", got)
	}
}

func TestBuildDatasetFreezesLabelsAndRejectsUnverifiedSessions(t *testing.T) {
	days := weekdayDays("2026-07-01", 70)
	asof := days[len(days)-1]
	cfg := config.DefaultDayRange()
	cfg.Enabled = true
	stocks := []config.Stock{{Code: "sz000001", Name: "测试", Sector: "银行"}}
	data := premarketData(days, "sz000001")
	dataset, err := BuildDataset(context.Background(), cfg, stocks, data, asof, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	last := dataset.Samples[len(dataset.Samples)-1]
	raw := data.Series[0].Raw[len(data.Series[0].Raw)-1]
	if last.Actual == nil || last.Actual.High != raw.High || last.Actual.Low != raw.Low || last.LabelSource != "daily_bar:tencent" {
		t.Fatalf("label did not use the completed raw target bar: %+v", last)
	}

	suspended := premarketData(days, "sz000001")
	suspended.Series[0].Raw = suspended.Series[0].Raw[:len(suspended.Series[0].Raw)-1]
	suspended.Series[0].Adjusted = suspended.Series[0].Adjusted[:len(suspended.Series[0].Adjusted)-1]
	suspended.Statuses["sz000001"][asof] = market.SessionStatus{Code: "sz000001", Date: asof, State: market.SessionSuspended, Source: "baostock"}
	result, err := BuildDataset(context.Background(), cfg, stocks, suspended, asof, time.Now())
	if err != nil || result.Samples[len(result.Samples)-1].UnresolvedReason != "target_suspended" {
		t.Fatalf("suspended target was accepted: %+v %v", result.Samples[len(result.Samples)-1], err)
	}

	mismatched := premarketData(days, "sz000001")
	mismatched.Series[0].RawSource[asof] = "sina"
	result, err = BuildDataset(context.Background(), cfg, stocks, mismatched, asof, time.Now())
	if err != nil || result.Samples[len(result.Samples)-1].UnresolvedReason != "source_inconsistent" {
		t.Fatalf("mixed raw sources were accepted: %+v %v", result.Samples[len(result.Samples)-1], err)
	}

	action := premarketData(days, "sz000001")
	lastAdjusted := &action.Series[0].Adjusted[len(action.Series[0].Adjusted)-1]
	lastAdjusted.Open *= .90
	lastAdjusted.Close *= .90
	lastAdjusted.High *= .90
	lastAdjusted.Low *= .90
	result, err = BuildDataset(context.Background(), cfg, stocks, action, asof, time.Now())
	if err != nil || result.Samples[len(result.Samples)-1].UnresolvedReason != "corporate_action" {
		t.Fatalf("corporate action was not isolated: %+v %v", result.Samples[len(result.Samples)-1], err)
	}
}
