// Package firststage implements the recall-first daily candidate screen.
// It is deliberately independent of the formal recommendation and only ranks
// the supplied qfq daily histories through the signal close.
package firststage

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
)

const (
	ReportSchemaVersion = 1
	StatusOK            = "ok"
	StatusEmpty         = "empty"
	StatusFailed        = "failed"
	MinBars             = 65
	HorizonDays         = 3
	TouchThresholdPct   = 1.0
)

const (
	featureATR14 = iota
	featureLow20Distance
	featureReturn5
	featureRSI14
	featureTouchRate20
	featureBaseScore
	featureVolumeRatio5
	featureHigh20Distance
	featureCount
)

type Options struct {
	AsOf        string
	Target      string
	Adjust      string
	Budget      int
	PushEnabled bool
	Now         time.Time
}

type Candidate struct {
	Rank       int             `json:"rank"`
	Code       string          `json:"code"`
	Name       string          `json:"name"`
	Sector     string          `json:"sector,omitempty"`
	Close      float64         `json:"close"`
	ChangePct  float64         `json:"change_pct"`
	BaseScore  float64         `json:"base_score"`
	ModelScore float64         `json:"model_score"`
	BaseRank   float64         `json:"base_rank"`
	ModelRank  float64         `json:"model_rank"`
	BlendScore float64         `json:"blend_score"`
	Features   FeatureSnapshot `json:"features"`
}

// FeatureSnapshot contains only information available through the signal-day
// close. Downstream rankers use the same facts instead of recomputing them.
type FeatureSnapshot struct {
	Return1         float64 `json:"return1"`
	Return5         float64 `json:"return5"`
	Return10        float64 `json:"return10"`
	RelativeReturn5 float64 `json:"relative_return5"`
	MA20Slope       float64 `json:"ma20_slope"`
	MA20Distance    float64 `json:"ma20_distance"`
	ATR14Pct        float64 `json:"atr14_pct"`
	RSI14           float64 `json:"rsi14"`
	VolumeRatio     float64 `json:"volume_ratio"`
	ClosePosition   float64 `json:"close_position"`
	UpperWick       float64 `json:"upper_wick"`
	High20Distance  float64 `json:"high20_distance"`
	Low20Distance   float64 `json:"low20_distance"`
	TouchRate20     float64 `json:"touch_rate20"`
	TrendScore      int     `json:"trend_score"`
}

type Excluded struct {
	Code     string `json:"code"`
	Reason   string `json:"reason"`
	LastDate string `json:"last_date,omitempty"`
}

type Report struct {
	Version        int         `json:"schema_version"`
	Status         string      `json:"status"`
	AsOf           string      `json:"as_of"`
	Target         string      `json:"target_trading_date"`
	GeneratedAt    time.Time   `json:"generated_at"`
	ModelID        string      `json:"model_id,omitempty"`
	ModelVersion   int         `json:"model_version,omitempty"`
	ModelSHA256    string      `json:"model_sha256,omitempty"`
	TrainedFrom    string      `json:"trained_from,omitempty"`
	TrainedThrough string      `json:"trained_through,omitempty"`
	HorizonDays    int         `json:"horizon_days"`
	TouchPct       float64     `json:"touch_threshold_pct"`
	Budget         int         `json:"budget"`
	PoolSize       int         `json:"pool_size"`
	Eligible       int         `json:"eligible"`
	PushEnabled    bool        `json:"push_enabled"`
	Candidates     []Candidate `json:"candidates"`
	Excluded       []Excluded  `json:"excluded"`
	Rules          []string    `json:"rules"`
	Reason         string      `json:"reason,omitempty"`
}

type row struct {
	code          string
	name          string
	sector        string
	close         float64
	changePct     float64
	trendScore    int
	ma20Slope     float64
	ma20Distance  float64
	closePosition float64
	upperWick     float64
	snapshot      FeatureSnapshot
	features      [featureCount]float64
	z             [featureCount]float64
	baseScore     float64
	model         float64
	baseRank      float64
	modelRank     float64
	blend         float64
}

func Analyze(histories []market.History, pool []config.Stock, opts Options) (Report, error) {
	report := baseReport(opts)
	model, err := DefaultModel()
	if err != nil {
		return failReport(report, err)
	}
	report.ModelID = model.ID
	report.ModelVersion = model.SchemaVersion
	report.ModelSHA256 = model.SHA256
	report.TrainedFrom = model.TrainFrom
	report.TrainedThrough = model.TrainThrough
	report.Rules[2] = blendRule(model)
	return analyzeWithModel(report, histories, pool, opts, model)
}

func analyzeWithModel(report Report, histories []market.History, pool []config.Stock, opts Options, model Model) (Report, error) {
	if report.AsOf == "" || report.Target == "" || opts.Adjust == "" {
		return failReport(report, errors.New("first-stage as_of, target and adjust are required"))
	}
	if _, err := time.Parse("2006-01-02", report.AsOf); err != nil {
		return failReport(report, errors.New("first-stage as_of must be a date"))
	}
	if _, err := time.Parse("2006-01-02", report.Target); err != nil {
		return failReport(report, errors.New("first-stage target must be a date"))
	}
	if opts.Budget < 1 || opts.Budget > 500 {
		return failReport(report, errors.New("first-stage budget must be between 1 and 500"))
	}
	if model.SchemaVersion != ModelSchemaVersion || model.ID != ModelID || len(model.Features) != len(modelFeatures) {
		return failReport(report, errors.New("unsupported first-stage model contract"))
	}
	poolCodes := make(map[string]bool, len(pool))
	for _, stock := range pool {
		if !config.ValidCode(stock.Code) || poolCodes[stock.Code] {
			return failReport(report, errors.New("first-stage pool contains an invalid or duplicate code"))
		}
		poolCodes[stock.Code] = true
	}
	byCode := make(map[string]market.History, len(histories))
	for _, history := range histories {
		if !poolCodes[history.Code] {
			return failReport(report, errors.New("first-stage history is outside the stock pool"))
		}
		if _, exists := byCode[history.Code]; exists {
			return failReport(report, errors.New("first-stage history contains a duplicate code"))
		}
		byCode[history.Code] = history
	}

	rows := make([]row, 0, len(pool))
	excluded := make([]Excluded, 0)
	for _, stock := range pool {
		history, ok := byCode[stock.Code]
		if !ok {
			excluded = append(excluded, Excluded{Code: stock.Code, Reason: "missing_data"})
			continue
		}
		if err := history.Validate(); err != nil {
			excluded = append(excluded, Excluded{Code: stock.Code, Reason: "invalid_history", LastDate: lastDate(history)})
			continue
		}
		if history.Version != 1 || history.Adjust != opts.Adjust || history.AsOf != report.AsOf {
			excluded = append(excluded, Excluded{Code: stock.Code, Reason: "invalid_or_stale_data", LastDate: lastDate(history)})
			continue
		}
		if len(history.Bars) < MinBars {
			excluded = append(excluded, Excluded{Code: stock.Code, Reason: "insufficient_history", LastDate: lastDate(history)})
			continue
		}
		last := history.Bars[len(history.Bars)-1]
		if last.Date != report.AsOf {
			excluded = append(excluded, Excluded{Code: stock.Code, Reason: "stale_or_suspended", LastDate: last.Date})
			continue
		}
		features, err := strategy.Extract(history.Bars)
		if err != nil {
			excluded = append(excluded, Excluded{Code: stock.Code, Reason: "invalid_or_insufficient_features", LastDate: last.Date})
			continue
		}
		low20, ok := priorLow20(history.Bars)
		if !ok {
			excluded = append(excluded, Excluded{Code: stock.Code, Reason: "invalid_numeric_input", LastDate: last.Date})
			continue
		}
		lowDistance := (last.Close/low20 - 1) * 100
		touchRate := touchRate20(history.Bars)
		candidate := row{
			code:          stock.Code,
			name:          strings.TrimSpace(firstNonEmpty(history.Name, stock.Name)),
			sector:        stock.Sector,
			close:         last.Close,
			changePct:     features.Return1,
			trendScore:    features.Metrics.Score,
			ma20Slope:     features.MA20Slope,
			ma20Distance:  features.MA20Distance,
			closePosition: features.ClosePosition,
			upperWick:     features.UpperWick,
			snapshot: FeatureSnapshot{
				Return1:        features.Return1,
				Return5:        features.Return5,
				Return10:       features.Return10,
				MA20Slope:      features.MA20Slope,
				MA20Distance:   features.MA20Distance,
				ATR14Pct:       features.ATR14Pct,
				RSI14:          features.Metrics.RSI14,
				VolumeRatio:    features.VolumeRatio,
				ClosePosition:  features.ClosePosition,
				UpperWick:      features.UpperWick,
				High20Distance: features.High20Distance,
				Low20Distance:  lowDistance,
				TouchRate20:    touchRate,
				TrendScore:     features.Metrics.Score,
			},
			features: [featureCount]float64{
				features.ATR14Pct,
				lowDistance,
				features.Return5,
				features.Metrics.RSI14,
				touchRate,
				0,
				features.VolumeRatio,
				features.High20Distance,
			},
		}
		for _, value := range candidate.features {
			if !finite(value) {
				excluded = append(excluded, Excluded{Code: stock.Code, Reason: "invalid_numeric_input", LastDate: last.Date})
				candidate.code = ""
				break
			}
		}
		if candidate.code != "" {
			candidate.features[featureBaseScore] = 0
			rows = append(rows, candidate)
		}
	}

	report.PoolSize = len(pool)
	report.Eligible = len(rows)
	report.Excluded = excluded
	if len(rows) == 0 {
		report.Status = StatusEmpty
		return report, nil
	}

	poolReturn5 := 0.0
	for i := range rows {
		poolReturn5 += rows[i].features[featureReturn5]
	}
	poolReturn5 /= float64(len(rows))
	for i := range rows {
		rows[i].baseScore = baseScore(rows[i].trendScore, rows[i].features, poolReturn5, rows[i].ma20Slope, rows[i].ma20Distance, rows[i].closePosition, rows[i].upperWick)
		rows[i].features[featureBaseScore] = rows[i].baseScore
	}
	for feature := range modelFeatures {
		mean, deviation := populationStats(rows, feature)
		for i := range rows {
			if deviation == 0 {
				rows[i].z[feature] = 0
			} else {
				rows[i].z[feature] = (rows[i].features[feature] - mean) / deviation
			}
		}
	}
	for i := range rows {
		rows[i].model = predict(model, rows[i].z)
		if !finite(rows[i].model) || !finite(rows[i].baseScore) {
			return failReport(report, errors.New("first-stage produced a non-finite score"))
		}
	}
	rankRows(rows, model.BaseWeight, model.ModelWeight, model.BlendOperator)
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].blend != rows[j].blend {
			return rows[i].blend > rows[j].blend
		}
		return rows[i].code < rows[j].code
	})

	budget := min(opts.Budget, len(rows))
	candidates := make([]Candidate, 0, budget)
	for i := 0; i < budget; i++ {
		item := rows[i]
		snapshot := item.snapshot
		snapshot.RelativeReturn5 = item.snapshot.Return5 - poolReturn5
		candidates = append(candidates, Candidate{
			Rank:       i + 1,
			Code:       item.code,
			Name:       item.name,
			Sector:     item.sector,
			Close:      item.close,
			ChangePct:  item.changePct,
			BaseScore:  item.baseScore,
			ModelScore: item.model,
			BaseRank:   item.baseRank,
			ModelRank:  item.modelRank,
			BlendScore: item.blend,
			Features:   snapshot,
		})
	}
	for _, item := range rows[budget:] {
		report.Excluded = append(report.Excluded, Excluded{Code: item.code, Reason: "outside_budget"})
	}
	report.Budget = opts.Budget
	report.PushEnabled = opts.PushEnabled
	report.Candidates = candidates
	if len(candidates) == 0 {
		report.Status = StatusEmpty
	} else {
		report.Status = StatusOK
	}
	return report, nil
}

func baseReport(opts Options) Report {
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	return Report{
		Version:     ReportSchemaVersion,
		Status:      StatusOK,
		AsOf:        opts.AsOf,
		Target:      opts.Target,
		GeneratedAt: now.In(market.Shanghai),
		HorizonDays: HorizonDays,
		TouchPct:    TouchThresholdPct,
		Budget:      opts.Budget,
		PushEnabled: opts.PushEnabled,
		Candidates:  []Candidate{},
		Excluded:    []Excluded{},
		Rules: []string{
			"标签口径：信号日收盘后，未来3个交易日最高价相对信号日收盘至少 +1%",
			"特征仅使用信号日及以前的前复权日线；横截面 z-score 以当日有效池为分母",
			"综合分 = 0.5 × 机会分日内排名 + 0.5 × logistic 模型分日内排名",
			"第一层只负责高召回候选池，仅供第二层精排，不构成交易建议",
		},
	}
}

func failReport(report Report, err error) (Report, error) {
	report.Status = StatusFailed
	report.Reason = err.Error()
	report.Candidates = []Candidate{}
	return report, err
}

func lastDate(history market.History) string {
	if len(history.Bars) == 0 {
		return ""
	}
	return history.Bars[len(history.Bars)-1].Date
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func priorLow20(bars []market.Bar) (float64, bool) {
	if len(bars) < 21 {
		return 0, false
	}
	low := math.Inf(1)
	for _, bar := range bars[len(bars)-21 : len(bars)-1] {
		if bar.Low <= 0 || !finite(bar.Low) {
			return 0, false
		}
		low = math.Min(low, bar.Low)
	}
	return low, finite(low) && low > 0
}

func touchRate20(bars []market.Bar) float64 {
	if len(bars) < 4 {
		return 0
	}
	lastAnchor := len(bars) - 4
	start := max(0, lastAnchor-19)
	hits := 0
	count := 0
	for anchor := start; anchor <= lastAnchor; anchor++ {
		close := bars[anchor].Close
		if close <= 0 {
			continue
		}
		high := math.Max(bars[anchor+1].High, math.Max(bars[anchor+2].High, bars[anchor+3].High))
		count++
		if high/close-1 >= TouchThresholdPct/100 {
			hits++
		}
	}
	if count == 0 {
		return 0
	}
	return float64(hits) / float64(count)
}

func baseScore(trendScore int, features [featureCount]float64, poolReturn5, ma20Slope, ma20Distance, closePosition, upperWick float64) float64 {
	value := float64(trendScore)*0.25 + 25
	if ma20Slope > 0 {
		value += 10
	}
	if features[featureReturn5]-poolReturn5 > 0 {
		value += 10
	}
	if closePosition >= 0.65 {
		value += 10
	}
	if features[featureVolumeRatio5] >= 1 && features[featureVolumeRatio5] <= 2.5 {
		value += 10
	}
	if features[featureATR14] >= 1 && features[featureATR14] <= 4 {
		value += 10
	}
	if ma20Distance > 10 || features[featureReturn5] > 12 {
		value -= 20
	}
	if upperWick > 0.5 {
		value -= 10
	}
	return math.Max(0, math.Min(100, value))
}

func populationStats(rows []row, feature int) (float64, float64) {
	mean := 0.0
	for i := range rows {
		mean += rows[i].features[feature]
	}
	mean /= float64(len(rows))
	variance := 0.0
	for i := range rows {
		delta := rows[i].features[feature] - mean
		variance += delta * delta
	}
	return mean, math.Sqrt(variance / float64(len(rows)))
}

func predict(model Model, z [featureCount]float64) float64 {
	value := model.Intercept
	for i := range modelFeatures {
		value += model.Coefficients[i] * ((z[i] - model.Means[i]) / model.Scales[i])
	}
	if value >= 0 {
		return 1 / (1 + math.Exp(-value))
	}
	exp := math.Exp(value)
	return exp / (1 + exp)
}

func rankRows(rows []row, baseWeight, modelWeight float64, operator string) {
	baseOrder := make([]int, len(rows))
	modelOrder := make([]int, len(rows))
	for i := range rows {
		baseOrder[i] = i
		modelOrder[i] = i
	}
	sort.SliceStable(baseOrder, func(i, j int) bool {
		left, right := rows[baseOrder[i]], rows[baseOrder[j]]
		if left.baseScore != right.baseScore {
			return left.baseScore < right.baseScore
		}
		return left.code < right.code
	})
	sort.SliceStable(modelOrder, func(i, j int) bool {
		left, right := rows[modelOrder[i]], rows[modelOrder[j]]
		if left.model != right.model {
			return left.model < right.model
		}
		return left.code < right.code
	})
	assignMidRanks(rows, baseOrder, func(index int, rank float64) {
		rows[index].baseRank = rank
	}, func(index int) float64 { return rows[index].baseScore })
	assignMidRanks(rows, modelOrder, func(index int, rank float64) {
		rows[index].modelRank = rank
	}, func(index int) float64 { return rows[index].model })
	if operator == "rank_gauss" {
		for i := range rows {
			baseZ := normalQuantile((rows[i].baseRank + 1) / (float64(len(rows)) + 1))
			modelZ := normalQuantile((rows[i].modelRank + 1) / (float64(len(rows)) + 1))
			rows[i].blend = normalCDF(baseWeight*baseZ + modelWeight*modelZ)
		}
		return
	}
	for i := range rows {
		rows[i].blend = baseWeight*rows[i].baseRank + modelWeight*rows[i].modelRank
	}
}

func blendRule(model Model) string {
	if model.BlendOperator == "rank_gauss" {
		return fmt.Sprintf("综合分 = Φ(%.2f × Φ⁻¹(机会分中位百分位) + %.2f × Φ⁻¹(logistic 模型中位百分位))", model.BaseWeight, model.ModelWeight)
	}
	return fmt.Sprintf("综合分 = %.2f × 机会分中位百分位 + %.2f × logistic 模型中位百分位", model.BaseWeight, model.ModelWeight)
}

func normalQuantile(p float64) float64 {
	return math.Sqrt2 * math.Erfinv(2*p-1)
}

func normalCDF(value float64) float64 {
	return 0.5 * (1 + math.Erf(value/math.Sqrt2))
}

// assignMidRanks gives every tied score the mean of the positions it spans.
// A tied group cannot gain an advantage from stock-code lexicographic order,
// and a one-row universe uses the neutral 0.5 percentile.
func assignMidRanks(rows []row, order []int, set func(index int, rank float64), score func(index int) float64) {
	if len(order) == 0 {
		return
	}
	if len(order) == 1 {
		set(order[0], 0.5)
		return
	}
	denominator := float64(len(order) - 1)
	for start := 0; start < len(order); {
		end := start
		value := score(order[start])
		for end+1 < len(order) && score(order[end+1]) == value {
			end++
		}
		rank := (float64(start) + float64(end)) / 2 / denominator
		for position := start; position <= end; position++ {
			set(order[position], rank)
		}
		start = end + 1
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
