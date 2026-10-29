// Package marketforecast fits a deterministic daily direction model for broad
// market indices. It is a research signal: predictions are probabilities, not
// recommendations, price targets, or execution instructions.
package marketforecast

const (
	SchemaVersion = 1

	ModelID        = "index_direction_logistic_v1"
	ModelVersion   = 1
	FeatureVersion = 1

	MinHistoryBars      = 65
	MinTrainingSamples  = 60
	MinValidationSample = 40
)

// Index identifies one supported broad market index.
type Index struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// DefaultIndices returns the two indices shown by the market signal. Other
// core indices remain available in context.db but do not receive a signal.
func DefaultIndices() []Index {
	return []Index{
		{Code: "sh000001", Name: "上证指数"},
		{Code: "sz399006", Name: "创业板指"},
	}
}

// Bar is the minimal daily index row used by the model. Volume is deliberately
// omitted because index volume conventions differ across providers.
type Bar struct {
	Date  string
	Open  float64
	Close float64
	High  float64
	Low   float64
}

// Series is one index history. Bars may arrive unsorted; Build normalizes and
// cuts them at the requested as-of date before fitting anything.
type Series struct {
	Index          Index
	Bars           []Bar
	Minutes        []MinuteBar
	SessionMinutes []MinuteBar
}

// Options controls one deterministic fit. NextTradingDay is supplied by the
// caller so the package does not duplicate exchange-calendar rules.
type Options struct {
	AsOf              string
	NextTradingDay    func(string) (string, error)
	TrainingSamples   int
	ValidationSamples int
	Korea             []KoreaSeries
	HangSeng          []Bar
}

type Report struct {
	SchemaVersion int              `json:"schema_version"`
	Status        string           `json:"status"`
	AsOf          string           `json:"as_of"`
	DataThrough   string           `json:"data_through,omitempty"`
	Model         ModelInfo        `json:"model"`
	SessionModel  SessionModelInfo `json:"session_model"`
	Signals       []Signal         `json:"signals"`
	Warnings      []string         `json:"warnings,omitempty"`
}

type ModelInfo struct {
	ID                   string   `json:"id"`
	Version              int      `json:"version"`
	FeatureVersion       int      `json:"feature_version"`
	FeatureNames         []string `json:"feature_names"`
	MinHistoryBars       int      `json:"min_history_bars"`
	MinTrainingSamples   int      `json:"min_training_samples"`
	MinValidationSamples int      `json:"min_validation_samples"`
}

type Signal struct {
	Code          string          `json:"code"`
	Name          string          `json:"name"`
	Status        string          `json:"status"`
	DataThrough   string          `json:"data_through,omitempty"`
	TargetDate    string          `json:"target_date,omitempty"`
	Close         float64         `json:"close"`
	ChangePct     float64         `json:"change_pct"`
	Direction     string          `json:"direction,omitempty"`
	UpProbability float64         `json:"up_probability"`
	Strength      string          `json:"strength,omitempty"`
	Stale         bool            `json:"stale"`
	Reason        string          `json:"reason,omitempty"`
	Training      TrainingInfo    `json:"training"`
	Validation    ValidationInfo  `json:"validation"`
	Weekdays      []WeekdayStat   `json:"weekdays"`
	Sessions      []SessionSignal `json:"sessions,omitempty"`
}

type SessionModelInfo struct {
	ID                      string   `json:"id"`
	Version                 int      `json:"version"`
	FeatureVersion          int      `json:"feature_version"`
	FeatureNames            []string `json:"feature_names"`
	HighConfidenceThreshold float64  `json:"high_confidence_threshold"`
	TargetAccuracy          float64  `json:"target_accuracy"`
	CalibrationMethod       string   `json:"calibration_method"`
	CalibrationMinSamples   int      `json:"calibration_min_samples"`
	MorningMaxDepth         int      `json:"morning_max_depth"`
	AfternoonMaxDepth       int      `json:"afternoon_max_depth"`
	MinLeaf                 int      `json:"min_leaf"`
	MinTrainingSamples      int      `json:"min_training_samples"`
	MinValidationSamples    int      `json:"min_validation_samples"`
	KoreaFeatures           bool     `json:"korea_features"`
	HangSengShadow          bool     `json:"hang_seng_shadow"`
	HangSengFeatureNames    []string `json:"hang_seng_feature_names,omitempty"`
	ShadowWindows           []int    `json:"shadow_windows,omitempty"`
}

type TrainingInfo struct {
	From    string `json:"from,omitempty"`
	To      string `json:"to,omitempty"`
	Samples int    `json:"samples"`
	Up      int    `json:"up"`
	Down    int    `json:"down"`
}

type ValidationInfo struct {
	Status           string  `json:"status"`
	From             string  `json:"from,omitempty"`
	To               string  `json:"to,omitempty"`
	Samples          int     `json:"samples"`
	Accuracy         float64 `json:"accuracy"`
	MajorityAccuracy float64 `json:"majority_accuracy"`
	WeekdayAccuracy  float64 `json:"weekday_accuracy"`
	Conclusion       string  `json:"conclusion,omitempty"`
}

type WeekdayStat struct {
	Weekday       int     `json:"weekday"`
	Label         string  `json:"label"`
	Samples       int     `json:"samples"`
	Up            int     `json:"up"`
	Down          int     `json:"down"`
	Flat          int     `json:"flat"`
	WinRate       float64 `json:"win_rate"`
	MeanReturnPct float64 `json:"mean_return_pct"`
	WilsonLow     float64 `json:"wilson_low"`
	WilsonHigh    float64 `json:"wilson_high"`
}
