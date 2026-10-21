package dayrange

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/modeleval"
)

const SnapshotSchemaVersion = 1
const FeatureVersion = 1
const BaselineModelID = "baseline_range_quantile_v1"
const baselineMethod = "trailing_quantile_60"
const baselineWindow = 60
const minimumBaselineWindow = 20

var featureNames = []string{
	"prev_range_pct", "prev_body_pct", "prev_upper_wick_pct", "prev_lower_wick_pct",
	"prev_close_location", "prev_return", "prev_gap", "range_mean_5", "range_mean_20",
	"range_mean_60", "atr14_pct", "return_1", "return_5", "return_20", "return_60",
	"volatility_5", "volatility_20", "volatility_60", "volume_ratio_5_20",
	"ma5_ma20_spread", "ma20_ma60_spread", "drawdown_20", "distance_high_60",
	"distance_low_60", "market_return_1", "market_return_5", "market_trend_20",
	"market_volatility_20", "market_breadth_5", "sector_return_5", "sector_return_20",
	"sector_relative_20", "sector_breadth_5",
}

func FeatureNames() []string { return append([]string(nil), featureNames...) }

type Feature struct {
	Name  string   `json:"name"`
	Value *float64 `json:"value,omitempty"`
}

type Availability struct {
	Stock  string `json:"stock"`
	Market string `json:"market"`
	Sector string `json:"sector"`
}

type Snapshot struct {
	SchemaVersion    int                        `json:"schema_version"`
	Code             string                     `json:"code"`
	Name             string                     `json:"name,omitempty"`
	Sector           string                     `json:"sector,omitempty"`
	TargetDate       string                     `json:"target_date"`
	Mode             Mode                       `json:"mode"`
	AsOf             time.Time                  `json:"as_of"`
	InputThrough     string                     `json:"input_through"`
	ReferencePrice   float64                    `json:"reference_price"`
	ScalePrice       float64                    `json:"scale_price,omitempty"`
	FeatureVersion   int                        `json:"feature_version"`
	Features         []Feature                  `json:"features"`
	Availability     Availability               `json:"availability"`
	MarketState      string                     `json:"market_state"`
	VolatilityBucket string                     `json:"volatility_bucket"`
	Baseline         *Forecast                  `json:"baseline,omitempty"`
	Slices           []modeleval.SliceDimension `json:"slices,omitempty"`
	CreatedAt        time.Time                  `json:"created_at"`
}

// MarshalJSON stores feature values by their versioned FeatureNames order
// instead of repeating 33 names in every training sample. This keeps the
// frozen dataset below the snapshot size limit without changing model inputs.
func (s Snapshot) MarshalJSON() ([]byte, error) {
	values := make([]*float64, len(s.Features))
	for i := range s.Features {
		values[i] = s.Features[i].Value
	}
	return json.Marshal(struct {
		SchemaVersion    int                        `json:"schema_version"`
		Code             string                     `json:"code"`
		Name             string                     `json:"name,omitempty"`
		Sector           string                     `json:"sector,omitempty"`
		TargetDate       string                     `json:"target_date"`
		Mode             Mode                       `json:"mode"`
		AsOf             time.Time                  `json:"as_of"`
		InputThrough     string                     `json:"input_through"`
		ReferencePrice   float64                    `json:"reference_price"`
		ScalePrice       float64                    `json:"scale_price,omitempty"`
		FeatureVersion   int                        `json:"feature_version"`
		FeatureValues    []*float64                 `json:"feature_values"`
		Availability     Availability               `json:"availability"`
		MarketState      string                     `json:"market_state"`
		VolatilityBucket string                     `json:"volatility_bucket"`
		Baseline         *Forecast                  `json:"baseline,omitempty"`
		Slices           []modeleval.SliceDimension `json:"slices,omitempty"`
		CreatedAt        time.Time                  `json:"created_at"`
	}{s.SchemaVersion, s.Code, s.Name, s.Sector, s.TargetDate, s.Mode, s.AsOf, s.InputThrough, s.ReferencePrice,
		s.ScalePrice, s.FeatureVersion, values, s.Availability, s.MarketState, s.VolatilityBucket, s.Baseline, s.Slices, s.CreatedAt})
}

func (s *Snapshot) UnmarshalJSON(data []byte) error {
	var wire struct {
		SchemaVersion    int                        `json:"schema_version"`
		Code             string                     `json:"code"`
		Name             string                     `json:"name,omitempty"`
		Sector           string                     `json:"sector,omitempty"`
		TargetDate       string                     `json:"target_date"`
		Mode             Mode                       `json:"mode"`
		AsOf             time.Time                  `json:"as_of"`
		InputThrough     string                     `json:"input_through"`
		ReferencePrice   float64                    `json:"reference_price"`
		ScalePrice       float64                    `json:"scale_price,omitempty"`
		FeatureVersion   int                        `json:"feature_version"`
		FeatureValues    []*float64                 `json:"feature_values"`
		Availability     Availability               `json:"availability"`
		MarketState      string                     `json:"market_state"`
		VolatilityBucket string                     `json:"volatility_bucket"`
		Baseline         *Forecast                  `json:"baseline,omitempty"`
		Slices           []modeleval.SliceDimension `json:"slices,omitempty"`
		CreatedAt        time.Time                  `json:"created_at"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if len(wire.FeatureValues) != 0 && len(wire.FeatureValues) != len(featureNames) {
		return errors.New("dayrange snapshot feature count mismatch")
	}
	s.SchemaVersion, s.Code, s.Name, s.Sector = wire.SchemaVersion, wire.Code, wire.Name, wire.Sector
	s.TargetDate, s.Mode, s.AsOf, s.InputThrough = wire.TargetDate, wire.Mode, wire.AsOf, wire.InputThrough
	s.ReferencePrice, s.ScalePrice, s.FeatureVersion = wire.ReferencePrice, wire.ScalePrice, wire.FeatureVersion
	s.Availability, s.MarketState, s.VolatilityBucket = wire.Availability, wire.MarketState, wire.VolatilityBucket
	s.Baseline, s.Slices, s.CreatedAt = wire.Baseline, wire.Slices, wire.CreatedAt
	s.Features = nil
	if len(wire.FeatureValues) > 0 {
		s.Features = make([]Feature, len(featureNames))
		for i, name := range featureNames {
			s.Features[i] = Feature{Name: name, Value: wire.FeatureValues[i]}
		}
	}
	return nil
}

func (s Snapshot) Values() []float64 {
	out := make([]float64, len(s.Features))
	for i := range out {
		out[i] = math.NaN()
		if s.Features[i].Value != nil {
			out[i] = *s.Features[i].Value
		}
	}
	return out
}

func (s Snapshot) Ready() bool {
	if s.Baseline == nil || s.ReferencePrice <= 0 || len(s.Features) != len(featureNames) {
		return false
	}
	return s.Availability.Stock == "available" || s.Availability.Market == "available" || s.Availability.Sector == "available"
}

func SnapshotID(s Snapshot) (string, error) {
	raw, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

type InputSeries struct {
	Stock          config.Stock
	Adjusted       []market.Bar
	Raw            []market.Bar
	AdjustedSource map[string]string
	RawSource      map[string]string
}

// MarketData is the immutable read surface needed to build a premarket snapshot.
// It contains only completed daily bars and session statuses; target-day future
// values are never read by the builder.
type MarketData struct {
	Series      []InputSeries
	TradingDays []string
	Statuses    map[string]map[string]market.SessionStatus
}

type SnapshotResult struct {
	Snapshot Snapshot `json:"snapshot"`
	Reason   string   `json:"reason,omitempty"`
}

type Sample struct {
	Snapshot         Snapshot  `json:"snapshot"`
	Actual           *Extremes `json:"actual,omitempty"`
	UnresolvedReason string    `json:"unresolved_reason,omitempty"`
	TrainingReason   string    `json:"training_reason,omitempty"`
	LabelSource      string    `json:"label_source,omitempty"`
}

type AuditDay struct {
	Date       string         `json:"date"`
	Resolved   int            `json:"resolved"`
	Unresolved int            `json:"unresolved"`
	Trainable  int            `json:"trainable"`
	Reasons    map[string]int `json:"reasons,omitempty"`
}

type Dataset struct {
	SchemaVersion int      `json:"schema_version"`
	AsOf          string   `json:"as_of"`
	From          string   `json:"from"`
	To            string   `json:"to"`
	Samples       []Sample `json:"samples"`
	Audit         Audit    `json:"audit"`
}

type Audit struct {
	SchemaVersion       int            `json:"schema_version"`
	AsOf                string         `json:"as_of"`
	From                string         `json:"from"`
	To                  string         `json:"to"`
	PoolSize            int            `json:"pool_size"`
	TradingDays         int            `json:"trading_days"`
	Candidates          int            `json:"candidates"`
	Resolved            int            `json:"resolved"`
	Unresolved          int            `json:"unresolved"`
	Trainable           int            `json:"trainable"`
	UnresolvedReasons   map[string]int `json:"unresolved_reasons,omitempty"`
	TrainingReasons     map[string]int `json:"training_reasons,omitempty"`
	FeatureAvailability map[string]int `json:"feature_availability"`
	RawSources          map[string]int `json:"raw_sources,omitempty"`
	AdjustedSources     map[string]int `json:"adjusted_sources,omitempty"`
	LabelSources        map[string]int `json:"label_sources,omitempty"`
	Days                []AuditDay     `json:"days,omitempty"`
}

func (ds Dataset) ID() (string, error) {
	raw, err := json.Marshal(ds)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func BuildDataset(ctx context.Context, cfg config.DayRange, stocks []config.Stock, data MarketData, asof string, now time.Time) (Dataset, error) {
	asofTime, err := time.ParseInLocation("2006-01-02", asof, shanghai)
	if err != nil || asofTime.Format("2006-01-02") != asof {
		return Dataset{}, errors.New("dayrange requires a valid completed trading date")
	}
	if err := ctx.Err(); err != nil {
		return Dataset{}, err
	}
	trading := append([]string(nil), data.TradingDays...)
	sort.Strings(trading)
	start := asofTime.AddDate(0, 0, -cfg.HistoryDays).Format("2006-01-02")
	active := make([]string, 0, len(trading))
	for _, day := range trading {
		if day >= start && day <= asof {
			active = append(active, day)
		}
	}
	if len(active) == 0 || active[len(active)-1] != asof {
		return Dataset{}, errors.New("dayrange calendar does not cover as-of date")
	}
	series, marketStats, sectorReturns := prepareContext(active, data.Series)
	audit := Audit{SchemaVersion: SnapshotSchemaVersion, AsOf: asof, From: start, To: asof, PoolSize: len(stocks),
		UnresolvedReasons: map[string]int{}, TrainingReasons: map[string]int{}, FeatureAvailability: map[string]int{},
		RawSources: map[string]int{}, AdjustedSources: map[string]int{}, LabelSources: map[string]int{}}
	audit.TradingDays = len(active)
	dayAudit := map[string]*AuditDay{}
	samples := make([]Sample, 0, len(active)*len(stocks))
	previous := ""
	for _, target := range active {
		if err := ctx.Err(); err != nil {
			return Dataset{}, err
		}
		day := dayAudit[target]
		if day == nil {
			day = &AuditDay{Date: target, Reasons: map[string]int{}}
			dayAudit[target] = day
		}
		if previous == "" {
			previous = target
			continue
		}
		refDate := previous
		for _, stock := range stocks {
			if err := ctx.Err(); err != nil {
				return Dataset{}, err
			}
			item, ok := series[stock.Code]
			if !ok {
				item = InputSeries{Stock: stock}
			}
			sample := buildSample(stock, item, target, refDate, asofTime, cfg, marketStats, sectorReturns, data.Statuses[stock.Code])
			audit.Candidates++
			resolved, unresolved, trainable := auditCounts(sample)
			day.Resolved += resolved
			day.Unresolved += unresolved
			day.Trainable += trainable
			if sample.Actual == nil {
				audit.Unresolved++
				audit.UnresolvedReasons[sample.UnresolvedReason]++
				day.Reasons[sample.UnresolvedReason]++
			} else {
				audit.Resolved++
				if sample.LabelSource != "" {
					audit.LabelSources[sample.LabelSource]++
				}
			}
			if sample.Actual != nil && sample.TrainingReason == "" {
				audit.Trainable++
			} else if sample.TrainingReason != "" {
				audit.TrainingReasons[sample.TrainingReason]++
			}
			audit.FeatureAvailability["stock:"+sample.Snapshot.Availability.Stock]++
			audit.FeatureAvailability["market:"+sample.Snapshot.Availability.Market]++
			audit.FeatureAvailability["sector:"+sample.Snapshot.Availability.Sector]++
			rawSource := item.RawSource[target]
			if rawSource != "" {
				audit.RawSources[rawSource]++
			}
			adjustedSource := item.AdjustedSource[target]
			if adjustedSource != "" {
				audit.AdjustedSources[adjustedSource]++
			}
			samples = append(samples, sample)
		}
		previous = target
	}
	audit.Days = make([]AuditDay, 0, len(dayAudit))
	for _, day := range dayAudit {
		audit.Days = append(audit.Days, *day)
	}
	sort.Slice(audit.Days, func(i, j int) bool { return audit.Days[i].Date < audit.Days[j].Date })
	return Dataset{SchemaVersion: SnapshotSchemaVersion, AsOf: asof, From: start, To: asof, Samples: samples, Audit: audit}, nil
}

func auditCounts(s Sample) (resolved, unresolved, trainable int) {
	if s.Actual == nil {
		return 0, 1, 0
	}
	if s.TrainingReason == "" {
		return 1, 0, 1
	}
	return 1, 0, 0
}

func buildSample(stock config.Stock, item InputSeries, target, ref string, asof time.Time, cfg config.DayRange, marketStats map[string]marketStats, sectorReturns map[string]map[string][]float64, statuses map[string]market.SessionStatus) Sample {
	rawByDate := barsByDate(item.Raw)
	adjByDate := barsByDate(item.Adjusted)
	rawTarget, targetBarOK := rawByDate[target]
	status, statusOK := statuses[target]
	if statusOK && status.State == market.SessionSuspended {
		if targetBarOK {
			return unresolvedSample(stock, target, ref, asof, "source_inconsistent")
		}
		return unresolvedSample(stock, target, ref, asof, "target_suspended")
	}
	if !statusOK || status.State != market.SessionTraded || !targetBarOK {
		return unresolvedSample(stock, target, ref, asof, "target_status_unknown")
	}
	snapshot, reason := makeSnapshot(stock, item, target, ref, cfg, marketStats, sectorReturns)
	if reason != "" {
		return Sample{Snapshot: snapshot, UnresolvedReason: reason}
	}
	rawRef := rawByDate[ref]
	if !validBar(rawTarget) || !validBar(rawRef) {
		return unresolvedSample(stock, target, ref, asof, "invalid_prices")
	}
	rawSource := item.RawSource[target]
	refSource := item.RawSource[ref]
	if rawSource == "" || refSource == "" || rawSource != refSource {
		return unresolvedSample(stock, target, ref, asof, "source_inconsistent")
	}
	adjTarget, adjTargetOK := adjByDate[target]
	adjRef := adjByDate[ref]
	if !adjTargetOK {
		return unresolvedSample(stock, target, ref, asof, "adjusted_history_missing")
	}
	if !validBar(adjRef) || !validBar(adjTarget) {
		return unresolvedSample(stock, target, ref, asof, "invalid_prices")
	}
	if corporateAction(rawRef, rawTarget, adjRef, adjTarget) {
		return unresolvedSample(stock, target, ref, asof, "corporate_action")
	}
	actual := &Extremes{High: rawTarget.High, Low: rawTarget.Low}
	trainingReason := ""
	if !snapshot.Ready() {
		trainingReason = "snapshot_not_ready"
	}
	return Sample{Snapshot: snapshot, Actual: actual, TrainingReason: trainingReason, LabelSource: "daily_bar:" + rawSource}
}

func makeSnapshot(stock config.Stock, item InputSeries, target, ref string, cfg config.DayRange, market map[string]marketStats, sectorReturns map[string]map[string][]float64) (Snapshot, string) {
	rawByDate := barsByDate(item.Raw)
	adjByDate := barsByDate(item.Adjusted)
	rawRef, rawOK := rawByDate[ref]
	if !rawOK {
		return Snapshot{}, "missing_reference"
	}
	adjRef, adjOK := adjByDate[ref]
	if !adjOK || !validBar(adjRef) {
		return Snapshot{}, "adjusted_history_missing"
	}
	baseline := baselineForecast(item.Raw, ref)
	features, availability, marketState, volBucket, slices := buildFeatures(stock, item.Raw, ref, cfg, market, sectorReturns)
	snapshot := Snapshot{SchemaVersion: SnapshotSchemaVersion, Code: stock.Code, Name: stock.Name, Sector: stock.Sector,
		TargetDate: target, Mode: ModePremarket, AsOf: premarketAsOf(target), InputThrough: ref,
		ReferencePrice: rawRef.Close, ScalePrice: volatilityScale(item.Raw, ref), FeatureVersion: FeatureVersion,
		Features: features, Availability: availability, MarketState: marketState, VolatilityBucket: volBucket,
		Baseline: baseline, Slices: slices, CreatedAt: premarketAsOf(target)}
	if !snapshot.Ready() {
		return snapshot, "snapshot_not_ready"
	}
	return snapshot, ""
}

// BuildPredictionSnapshots freezes the premarket view for a target date. It
// never asks for a bar newer than the previous completed session.
func BuildPredictionSnapshots(ctx context.Context, cfg config.DayRange, stocks []config.Stock, data MarketData, target string, now time.Time) ([]SnapshotResult, error) {
	targetTime, err := time.ParseInLocation("2006-01-02", target, shanghai)
	if err != nil || targetTime.Format("2006-01-02") != target {
		return nil, errors.New("dayrange target date is invalid")
	}
	trading := append([]string(nil), data.TradingDays...)
	sort.Strings(trading)
	index := sort.SearchStrings(trading, target)
	if index >= len(trading) || trading[index] != target || index == 0 {
		return nil, errors.New("dayrange target date is not a covered trading day")
	}
	ref := trading[index-1]
	series, marketStats, sectorReturns := prepareContext(trading[:index], data.Series)
	out := make([]SnapshotResult, 0, len(stocks))
	for _, stock := range stocks {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		item, ok := series[stock.Code]
		if !ok {
			item = InputSeries{Stock: stock}
		}
		snapshot, reason := makeSnapshot(stock, item, target, ref, cfg, marketStats, sectorReturns)
		if reason == "" {
			generated := now.In(shanghai)
			if now.IsZero() {
				generated = premarketAsOf(target)
			}
			snapshot.AsOf = generated
			snapshot.CreatedAt = generated
		}
		out = append(out, SnapshotResult{Snapshot: snapshot, Reason: reason})
	}
	return out, nil
}

func prepareContext(active []string, input []InputSeries) (map[string]InputSeries, map[string]marketStats, map[string]map[string][]float64) {
	series := make(map[string]InputSeries, len(input))
	marketReturns := map[string]map[string]float64{}
	sectorReturns := map[string]map[string][]float64{}
	for _, item := range input {
		series[item.Stock.Code] = item
		marketReturns[item.Stock.Code] = dailyReturns(item.Raw)
		for date, ret := range marketReturns[item.Stock.Code] {
			if item.Stock.Sector != "" {
				if sectorReturns[item.Stock.Sector] == nil {
					sectorReturns[item.Stock.Sector] = map[string][]float64{}
				}
				sectorReturns[item.Stock.Sector][date] = append(sectorReturns[item.Stock.Sector][date], ret)
			}
		}
	}
	return series, buildMarketStats(active, input, marketReturns), sectorReturns
}

func unresolvedSample(stock config.Stock, target, ref string, asof time.Time, reason string) Sample {
	return Sample{Snapshot: Snapshot{SchemaVersion: SnapshotSchemaVersion, Code: stock.Code, Name: stock.Name, Sector: stock.Sector,
		TargetDate: target, Mode: ModePremarket, AsOf: premarketAsOf(target), InputThrough: ref, FeatureVersion: FeatureVersion,
		CreatedAt: premarketAsOf(target)}, UnresolvedReason: reason}
}

func premarketAsOf(target string) time.Time {
	day, _ := time.ParseInLocation("2006-01-02", target, shanghai)
	return day.Add(9 * time.Hour)
}

func barsByDate(bars []market.Bar) map[string]market.Bar {
	out := make(map[string]market.Bar, len(bars))
	for _, bar := range bars {
		out[bar.Date] = bar
	}
	return out
}

func validBar(bar market.Bar) bool {
	return bar.Open > 0 && bar.Close > 0 && bar.High >= math.Max(bar.Open, bar.Close) && bar.Low > 0 && bar.Low <= math.Min(bar.Open, bar.Close)
}

func corporateAction(rawRef, rawTarget, adjRef, adjTarget market.Bar) bool {
	rawGap := math.Log(rawTarget.Open / rawRef.Close)
	adjGap := math.Log(adjTarget.Open / adjRef.Close)
	return math.Abs(rawGap-adjGap) > 0.03
}

func volatilityScale(bars []market.Bar, through string) float64 {
	selected := barsThrough(bars, through)
	if len(selected) < 2 {
		return 0
	}
	sum := 0.0
	n := 0
	for i := 1; i < len(selected); i++ {
		prev := selected[i-1]
		cur := selected[i]
		tr := math.Max(cur.High-cur.Low, math.Max(math.Abs(cur.High-prev.Close), math.Abs(cur.Low-prev.Close)))
		sum += tr
		n++
		if n == 14 {
			break
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

func baselineForecast(raw []market.Bar, refDate string) *Forecast {
	bars := barsThrough(raw, refDate)
	if len(bars) > baselineWindow {
		bars = bars[len(bars)-baselineWindow:]
	}
	if len(bars) < minimumBaselineWindow {
		return nil
	}
	highs := make([]float64, 0, len(bars)-1)
	lows := make([]float64, 0, len(bars)-1)
	for i := 1; i < len(bars); i++ {
		prev := bars[i-1]
		if !validBar(prev) || !validBar(bars[i]) || prev.Close <= 0 {
			continue
		}
		highs = append(highs, math.Log(bars[i].High/prev.Close))
		lows = append(lows, math.Log(bars[i].Low/prev.Close))
	}
	if len(highs) < minimumBaselineWindow-1 {
		return nil
	}
	ref := bars[len(bars)-1].Close
	highQ10, highQ50, highQ90 := quantile(highs, .1), quantile(highs, .5), quantile(highs, .9)
	lowQ10, lowQ50, lowQ90 := quantile(lows, .1), quantile(lows, .5), quantile(lows, .9)
	highPoint := math.Max(ref*math.Exp(highQ50), ref)
	lowPoint := math.Min(ref*math.Exp(lowQ50), highPoint)
	if lowPoint <= 0 {
		return nil
	}
	return &Forecast{
		High: Estimate{Point: highPoint, Quantiles: []Quantile{{Level: .1, Price: ref * math.Exp(highQ10)}, {Level: .5, Price: highPoint}, {Level: .9, Price: ref * math.Exp(highQ90)}}, Interval: &Interval{Lower: ref * math.Exp(highQ10), Upper: ref * math.Exp(highQ90), NominalCoverage: .8, Method: baselineMethod}},
		Low:  Estimate{Point: lowPoint, Quantiles: []Quantile{{Level: .1, Price: ref * math.Exp(lowQ10)}, {Level: .5, Price: lowPoint}, {Level: .9, Price: ref * math.Exp(lowQ90)}}, Interval: &Interval{Lower: ref * math.Exp(lowQ10), Upper: ref * math.Exp(lowQ90), NominalCoverage: .8, Method: baselineMethod}},
	}
}

func quantile(values []float64, q float64) float64 {
	if len(values) == 0 {
		return 0
	}
	copied := append([]float64(nil), values...)
	sort.Float64s(copied)
	index := int(math.Ceil(q*float64(len(copied)))) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(copied) {
		index = len(copied) - 1
	}
	return copied[index]
}

func barsThrough(bars []market.Bar, through string) []market.Bar {
	out := make([]market.Bar, 0, len(bars))
	for _, bar := range bars {
		if bar.Date <= through {
			out = append(out, bar)
		}
	}
	return out
}

func dailyReturns(bars []market.Bar) map[string]float64 {
	out := map[string]float64{}
	previous := 0.0
	for _, bar := range bars {
		if previous > 0 && bar.Close > 0 && bar.Date != "" {
			out[bar.Date] = math.Log(bar.Close / previous)
		}
		previous = bar.Close
	}
	return out
}

type marketStats struct {
	available bool
	return1   float64
	return5   float64
	trend20   float64
	vol20     float64
	breadth5  float64
}

func buildMarketStats(days []string, series []InputSeries, returns map[string]map[string]float64) map[string]marketStats {
	daily := make(map[string][]float64, len(days))
	for _, day := range days {
		for _, item := range series {
			if value, ok := returns[item.Stock.Code][day]; ok && finite(value) {
				daily[day] = append(daily[day], value)
			}
		}
	}
	averages := make(map[string]float64, len(days))
	breadth := make(map[string]float64, len(days))
	for _, day := range days {
		values := daily[day]
		if len(values) == 0 {
			continue
		}
		sum, positive := 0.0, 0
		for _, value := range values {
			sum += value
			if value > 0 {
				positive++
			}
		}
		averages[day] = sum / float64(len(values))
		breadth[day] = float64(positive) / float64(len(values))
	}
	out := map[string]marketStats{}
	for i, day := range days {
		if i < 20 {
			out[day] = marketStats{}
			continue
		}
		values := make([]float64, 0, 20)
		for _, previous := range days[i-20 : i] {
			if value, ok := averages[previous]; ok {
				values = append(values, value)
			}
		}
		if len(values) < 20 {
			out[day] = marketStats{}
			continue
		}
		mean := mean(values)
		last5 := meanOf(averages, days[i-5:i])
		out[day] = marketStats{available: true, return1: averages[day], return5: last5, trend20: mean,
			vol20: std(values), breadth5: meanOf(breadth, days[i-5:i])}
	}
	return out
}

func meanOf(values map[string]float64, days []string) float64 {
	sum, n := 0.0, 0
	for _, day := range days {
		if value, ok := values[day]; ok {
			sum += value
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

func mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sum := 0.0
	for _, value := range values {
		sum += value
	}
	return sum / float64(len(values))
}

func std(values []float64) float64 {
	if len(values) < 2 {
		return 0
	}
	m := mean(values)
	sum := 0.0
	for _, value := range values {
		d := value - m
		sum += d * d
	}
	return math.Sqrt(sum / float64(len(values)-1))
}

func buildFeatures(stock config.Stock, adjusted []market.Bar, ref string, cfg config.DayRange, market map[string]marketStats, sectors map[string]map[string][]float64) ([]Feature, Availability, string, string, []modeleval.SliceDimension) {
	features := make([]Feature, len(featureNames))
	for i, name := range featureNames {
		features[i] = Feature{Name: name}
	}
	availability := Availability{Stock: "disabled", Market: "disabled", Sector: "disabled"}
	bars := barsThrough(adjusted, ref)
	stockValues := stockFeatureValues(bars)
	if cfg.UseStockFeatures {
		availability.Stock = "insufficient_history"
		if stockValues != nil {
			availability.Stock = "available"
			copyFeatureValues(features, 0, stockValues)
		}
	}
	marketState := "unknown"
	if cfg.UseMarketFeatures {
		stats, ok := market[ref]
		availability.Market = "insufficient_history"
		if ok && stats.available {
			availability.Market = "available"
			copyFeatureValues(features, 24, []float64{stats.return1, stats.return5, stats.trend20, stats.vol20, stats.breadth5})
			switch {
			case stats.trend20 > .02:
				marketState = "up"
			case stats.trend20 < -.02:
				marketState = "down"
			default:
				marketState = "range"
			}
		}
	}
	sectorValues := make([]float64, 4)
	if cfg.UseSectorFeatures {
		availability.Sector = "insufficient_history"
		if stock.Sector != "" && stockValues != nil {
			if values, ok := sectorFeatureValues(sectors[stock.Sector], ref, stockValues[13]); ok {
				availability.Sector = "available"
				sectorValues = values
				copyFeatureValues(features, 29, sectorValues)
			}
		} else {
			availability.Sector = "missing_sector"
		}
	}
	volBucket := "unknown"
	if stockValues != nil {
		switch {
		case stockValues[10] < .02:
			volBucket = "low"
		case stockValues[10] < .04:
			volBucket = "medium"
		default:
			volBucket = "high"
		}
	}
	slices := []modeleval.SliceDimension{{Name: "code", Value: stock.Code}, {Name: "market_state", Value: marketState}, {Name: "volatility", Value: volBucket}}
	return features, availability, marketState, volBucket, slices
}

func copyFeatureValues(dst []Feature, offset int, values []float64) {
	for i, value := range values {
		if offset+i >= len(dst) || math.IsNaN(value) || math.IsInf(value, 0) {
			continue
		}
		v := value
		dst[offset+i].Value = &v
	}
}

func stockFeatureValues(bars []market.Bar) []float64 {
	if len(bars) < 61 {
		return nil
	}
	last := bars[len(bars)-1]
	prev := bars[len(bars)-2]
	if !validBar(last) || !validBar(prev) {
		return nil
	}
	values := make([]float64, 24)
	values[0] = (prev.High - prev.Low) / prev.Close
	values[1] = math.Abs(prev.Close-prev.Open) / prev.Close
	values[2] = (prev.High - math.Max(prev.Open, prev.Close)) / prev.Close
	values[3] = (math.Min(prev.Open, prev.Close) - prev.Low) / prev.Close
	rangePrev := prev.High - prev.Low
	if rangePrev > 0 {
		values[4] = (prev.Close - prev.Low) / rangePrev
	} else {
		values[4] = .5
	}
	values[5] = math.Log(last.Close / prev.Close)
	values[6] = math.Log(last.Open / prev.Close)
	values[7] = meanRange(bars, 5)
	values[8] = meanRange(bars, 20)
	values[9] = meanRange(bars, 60)
	values[10] = atrPercent(bars, 14)
	values[11] = ret(bars, 1)
	values[12] = ret(bars, 5)
	values[13] = ret(bars, 20)
	values[14] = ret(bars, 60)
	values[15] = volatility(bars, 5)
	values[16] = volatility(bars, 20)
	values[17] = volatility(bars, 60)
	values[18] = volumeRatio(bars, 5, 20)
	values[19] = math.Log(meanClose(bars, 5) / meanClose(bars, 20))
	values[20] = math.Log(meanClose(bars, 20) / meanClose(bars, 60))
	values[21] = last.Close/highest(bars, 20) - 1
	values[22] = last.Close/highest(bars, 60) - 1
	values[23] = last.Close/lowest(bars, 60) - 1
	return values
}

func meanRange(bars []market.Bar, n int) float64 {
	if len(bars) < n || n <= 0 {
		return 0
	}
	sum := 0.0
	for _, bar := range bars[len(bars)-n:] {
		if bar.Close > 0 {
			sum += (bar.High - bar.Low) / bar.Close
		}
	}
	return sum / float64(n)
}

func atrPercent(bars []market.Bar, n int) float64 {
	if len(bars) < n+1 {
		return 0
	}
	sum := 0.0
	for i := len(bars) - n; i < len(bars); i++ {
		prev, cur := bars[i-1], bars[i]
		tr := math.Max(cur.High-cur.Low, math.Max(math.Abs(cur.High-prev.Close), math.Abs(cur.Low-prev.Close)))
		sum += tr / cur.Close
	}
	return sum / float64(n)
}

func ret(bars []market.Bar, n int) float64 {
	if len(bars) <= n || bars[len(bars)-1-n].Close <= 0 {
		return 0
	}
	return math.Log(bars[len(bars)-1].Close / bars[len(bars)-1-n].Close)
}

func volatility(bars []market.Bar, n int) float64 {
	if len(bars) <= n {
		return 0
	}
	values := make([]float64, 0, n)
	for i := len(bars) - n; i < len(bars); i++ {
		if bars[i-1].Close > 0 {
			values = append(values, math.Log(bars[i].Close/bars[i-1].Close))
		}
	}
	return std(values)
}

func volumeRatio(bars []market.Bar, short, long int) float64 {
	shortMean := meanVolume(bars, short)
	longMean := meanVolume(bars, long)
	if longMean <= 0 {
		return 1
	}
	return shortMean / longMean
}

func meanVolume(bars []market.Bar, n int) float64 {
	if len(bars) < n {
		return 0
	}
	sum := 0.0
	for _, bar := range bars[len(bars)-n:] {
		sum += bar.Volume
	}
	return sum / float64(n)
}

func meanClose(bars []market.Bar, n int) float64 {
	if len(bars) < n {
		return 0
	}
	sum := 0.0
	for _, bar := range bars[len(bars)-n:] {
		sum += bar.Close
	}
	return sum / float64(n)
}

func highest(bars []market.Bar, n int) float64 {
	if len(bars) < n {
		return 0
	}
	value := bars[len(bars)-n].High
	for _, bar := range bars[len(bars)-n:] {
		if bar.High > value {
			value = bar.High
		}
	}
	return value
}

func lowest(bars []market.Bar, n int) float64 {
	if len(bars) < n {
		return 0
	}
	value := bars[len(bars)-n].Low
	for _, bar := range bars[len(bars)-n:] {
		if bar.Low < value {
			value = bar.Low
		}
	}
	return value
}

func sectorFeatureValues(sector map[string][]float64, ref string, stockReturn20 float64) ([]float64, bool) {
	if len(sector) == 0 {
		return nil, false
	}
	days := make([]string, 0, 20)
	for day := range sector {
		if day <= ref {
			days = append(days, day)
		}
	}
	sort.Strings(days)
	if len(days) < 20 {
		return nil, false
	}
	recent := days[len(days)-20:]
	return5 := sectorMean(sector, recent[len(recent)-5:])
	return20 := sectorMean(sector, recent)
	breadth5 := 0.0
	for _, day := range recent[len(recent)-5:] {
		values := sector[day]
		if len(values) == 0 {
			continue
		}
		positive := 0
		for _, value := range values {
			if value > 0 {
				positive++
			}
		}
		breadth5 += float64(positive) / float64(len(values))
	}
	breadth5 /= 5
	return []float64{return5, return20, stockReturn20 - return20, breadth5}, true
}

func sectorMean(sector map[string][]float64, days []string) float64 {
	all := []float64{}
	for _, day := range days {
		all = append(all, sector[day]...)
	}
	return mean(all)
}

func unresolvedReasonDictionary() []string {
	return []string{"calendar_unavailable", "missing_reference", "target_suspended", "target_status_unknown", "source_inconsistent", "adjusted_history_missing", "corporate_action", "invalid_prices", "missing_sector", "insufficient_history", "snapshot_not_ready"}
}

// ReasonDictionary is returned by the audit surface so callers do not invent a
// second vocabulary for missing data.
func ReasonDictionary() []string {
	return strings.Split(strings.Join(unresolvedReasonDictionary(), ","), ",")
}
