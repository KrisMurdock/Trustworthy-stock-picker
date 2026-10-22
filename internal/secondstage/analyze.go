package secondstage

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/firststage"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

// ValidateModelInputs guards frozen snapshots supplied by the framework. It
// mirrors the artifact checks so neither a malformed caller nor a corrupt file
// can reach percentile construction.
func ValidateModelInputs(inputs []ModelInput) error {
	seen := map[string]bool{}
	for _, input := range inputs {
		if !validModelName(input.Name) {
			return fmt.Errorf("second-stage model name %q is invalid", input.Name)
		}
		if seen[input.Name] {
			return fmt.Errorf("second-stage model %q is duplicated", input.Name)
		}
		seen[input.Name] = true
		switch input.Status {
		case ModelStatusOK:
			if len(input.Scores) == 0 {
				return fmt.Errorf("second-stage model %q snapshot is empty", input.Name)
			}
		case ModelStatusNil, ModelStatusBad:
			if len(input.Scores) > 0 {
				return fmt.Errorf("second-stage model %q snapshot carries scores without an ok status", input.Name)
			}
			continue
		default:
			return fmt.Errorf("second-stage model %q snapshot has an unknown status", input.Name)
		}
		for code, score := range input.Scores {
			if strings.TrimSpace(code) == "" || !finite(score.Adjacent15) || !finite(score.Full15Top12) || score.Adjacent15 < 0 || score.Full15Top12 < 0 || score.Adjacent15 > 1 || score.Full15Top12 > 1 {
				return fmt.Errorf("second-stage model %q score for %q is invalid", input.Name, code)
			}
		}
	}
	return nil
}

// ValidateJEVInput is the compatibility wrapper for the original single-model
// boundary. New callers should use ValidateModelInputs.
func ValidateJEVInput(in *JEVInput) error {
	if in == nil {
		return nil
	}
	input := *in
	if input.Name == "" {
		input.Name = ModelJEV
	}
	return ValidateModelInputs([]ModelInput{input})
}

func (plan BlendPlan) Validate() error {
	if strings.TrimSpace(plan.ID) == "" {
		return errors.New("second-stage blend id is required")
	}
	if !finite(plan.FirstStageWeight) || plan.FirstStageWeight < 0 || plan.FirstStageWeight > 1 {
		return errors.New("second-stage first-stage weight must be between 0 and 1")
	}
	if len(plan.Models) == 0 {
		return errors.New("second-stage blend models must not be empty")
	}
	seen := map[string]bool{}
	total := plan.FirstStageWeight
	for _, model := range plan.Models {
		if !validModelName(model.Name) {
			return fmt.Errorf("second-stage blend model name %q is invalid", model.Name)
		}
		if seen[model.Name] {
			return fmt.Errorf("second-stage blend model %q is duplicated", model.Name)
		}
		seen[model.Name] = true
		if !finite(model.Weight) || model.Weight <= 0 || model.Weight > 1 {
			return fmt.Errorf("second-stage blend model %q weight must be greater than 0 and at most 1", model.Name)
		}
		total += model.Weight
	}
	if math.Abs(total-1) > 1e-9 {
		return fmt.Errorf("second-stage blend weights must sum to 1, got %.6f", total)
	}
	return nil
}

func validModelName(name string) bool {
	if name == "" || len(name) > 64 || name[0] < 'a' || name[0] > 'z' {
		return false
	}
	for _, char := range name {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '.' || char == '_' || char == '-' {
			continue
		}
		return false
	}
	return !strings.Contains(name, "..")
}

func Analyze(first firststage.Report, opts Options) (Report, error) {
	report := baseReport(opts)
	if err := ValidateModelInputs(opts.Models); err != nil {
		return fail(report, err)
	}
	if opts.TopN <= 0 {
		opts.TopN = 10
		report.TopN = 10
	}
	if opts.TopN > 100 {
		return fail(report, errors.New("second-stage top_n must be between 1 and 100"))
	}
	if first.Version != firststage.ReportSchemaVersion || first.AsOf != opts.AsOf || first.Target != opts.Target || first.Status != firststage.StatusOK || len(first.Candidates) == 0 {
		return fail(report, errors.New("second-stage requires a matching non-empty first-stage report"))
	}
	model, err := DefaultModel()
	if err != nil {
		return fail(report, err)
	}
	return analyzeWithModel(report, first, opts, model)
}

func analyzeWithModel(report Report, first firststage.Report, opts Options, model Model) (Report, error) {
	plan := opts.Blend
	if strings.TrimSpace(plan.ID) == "" {
		plan = DefaultBlendPlan()
	}
	if err := plan.Validate(); err != nil {
		return fail(report, err)
	}
	if err := ValidateModelInputs(opts.Models); err != nil {
		return fail(report, err)
	}
	opts.Blend = plan
	report.ModelID = model.ModelID
	report.ModelSHA256 = model.SHA256
	report.TrainedFrom = model.TrainFrom
	report.TrainedThrough = model.TrainThrough
	report.InputCount = len(first.Candidates)
	report.Ranker = RankerBlendWeighted
	report.Blend = plan

	inputs := make(map[string]ModelInput, len(opts.Models))
	for _, input := range opts.Models {
		inputs[input.Name] = input
	}
	planNames := map[string]bool{}
	for _, weight := range plan.Models {
		planNames[weight.Name] = true
	}
	for _, input := range opts.Models {
		if !planNames[input.Name] {
			return fail(report, fmt.Errorf("second-stage model %q is not in the blend plan", input.Name))
		}
	}
	report.Models = make([]ModelStatus, 0, len(plan.Models))
	for _, weight := range plan.Models {
		input, ok := inputs[weight.Name]
		status := ModelStatusNil
		if ok && input.Status != "" {
			status = input.Status
		}
		if status != ModelStatusOK {
			report.Degraded = true
			report.Rules = append(report.Rules, fmt.Sprintf("Offline model %s is unavailable or invalid; it contributes a neutral 0.5 percentile and the result is degraded.", weight.Name))
		}
		report.Models = append(report.Models, ModelStatus{Name: weight.Name, Weight: weight.Weight, Status: status})
	}
	if jev, ok := inputs[ModelJEV]; ok {
		report.JEVStatus = jev.Status
	} else {
		report.JEVStatus = ModelStatusNil
	}

	poolReturn5 := 0.0
	for _, candidate := range first.Candidates {
		poolReturn5 += candidate.Features.Return5
	}
	poolReturn5 /= float64(len(first.Candidates))
	modelStats := make(map[string]map[int]jevGroupStats, len(plan.Models))
	modelPercentiles := make(map[string]map[string]float64, len(plan.Models))
	for _, weight := range plan.Models {
		input := inputs[weight.Name]
		if input.Status != ModelStatusOK {
			continue
		}
		groups := buildJEVGroupStats(first.Candidates, input.Scores)
		percentiles := make(map[string]float64, len(input.Scores))
		for index, candidate := range first.Candidates {
			group := candidateGroup(candidate, index)
			if _, ok := input.Scores[candidate.Code]; !ok {
				continue
			}
			if percentile, ok := groups[group].Percentile[candidate.Code]; ok {
				percentiles[candidate.Code] = percentile
			}
		}
		modelStats[weight.Name] = groups
		modelPercentiles[weight.Name] = percentiles
		if len(percentiles) < len(first.Candidates) {
			report.Degraded = true
			report.Rules = append(report.Rules, fmt.Sprintf("Offline model %s does not cover every first-stage candidate; uncovered rows use a neutral 0.5 percentile.", weight.Name))
		}
	}
	for index := range report.Models {
		report.Models[index].Covered = len(modelPercentiles[report.Models[index].Name])
	}
	firstStagePct := firstStagePercentilesByCode(first.Candidates)
	rankDenominator := len(first.Candidates) - 1
	if rankDenominator < 1 {
		rankDenominator = 1
	}

	rows := make([]Candidate, 0, len(first.Candidates))
	for index, candidate := range first.Candidates {
		group := candidateGroup(candidate, index)
		rankPosition := float64(index) / float64(rankDenominator)
		if candidate.Rank > 0 {
			rankPosition = float64(candidate.Rank-1) / float64(rankDenominator)
		}
		firstStagePercentile := firstStagePct[candidate.Code]
		blendScore := plan.FirstStageWeight * firstStagePercentile
		signals := make(map[string]ModelSignal, len(plan.Models))
		jevSignal := ModelSignal{Percentile: 0.5}
		for _, weight := range plan.Models {
			input := inputs[weight.Name]
			signal := ModelSignal{Percentile: 0.5}
			if input.Status == ModelStatusOK {
				if score, ok := input.Scores[candidate.Code]; ok {
					if percentile, ok := modelPercentiles[weight.Name][candidate.Code]; ok {
						signal = ModelSignal{Adjacent15: score.Adjacent15, Full15Top12: score.Full15Top12, Percentile: percentile, Present: true}
					}
				}
			}
			signals[weight.Name] = signal
			if weight.Name == ModelJEV {
				jevSignal = signal
			}
			blendScore += weight.Weight * signal.Percentile
		}
		jevStats := jevGroupStats{Percentile: map[string]float64{}}
		if stats, ok := modelStats[ModelJEV][group]; ok {
			jevStats = stats
		}
		values := featureVector(candidate, ModelScore{Adjacent15: jevSignal.Adjacent15, Full15Top12: jevSignal.Full15Top12}, jevSignal.Present, jevStats, rankPosition, poolReturn5)
		prediction, err := model.Predict(values)
		if err != nil {
			return fail(report, fmt.Errorf("second-stage candidate %s: %w", candidate.Code, err))
		}
		row := Candidate{
			Code:                 candidate.Code,
			Name:                 candidate.Name,
			Sector:               candidate.Sector,
			FirstStageRank:       candidate.Rank,
			FirstStageBlend:      candidate.BlendScore,
			JEVAdjacent15:        jevSignal.Adjacent15,
			JEVFull15Top12:       jevSignal.Full15Top12,
			JEVPresent:           jevSignal.Present,
			FirstStagePercentile: firstStagePercentile,
			JEVPercentile:        jevSignal.Percentile,
			Models:               signals,
			BlendScore:           blendScore,
			PredictedMaxHigh:     prediction,
			Features:             candidate.Features,
		}
		for _, weight := range plan.Models {
			if !signals[weight.Name].Present {
				reason := "model_missing:" + weight.Name
				if weight.Name == ModelJEV {
					reason = "jev_missing"
				}
				row.Reasons = append(row.Reasons, reason)
			}
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].BlendScore != rows[j].BlendScore {
			return rows[i].BlendScore > rows[j].BlendScore
		}
		if rows[i].FirstStageRank != rows[j].FirstStageRank {
			return rows[i].FirstStageRank < rows[j].FirstStageRank
		}
		return rows[i].Code < rows[j].Code
	})
	topN := opts.TopN
	if topN > len(rows) {
		topN = len(rows)
	}
	for index := 0; index < topN; index++ {
		rows[index].Rank = index + 1
	}
	report.Candidates = rows[:topN]
	for _, row := range rows[topN:] {
		report.Excluded = append(report.Excluded, Excluded{Code: row.Code, Reason: "outside_top_n"})
	}
	report.Status = StatusOK
	return report, nil
}

func firstStagePercentilesByCode(candidates []firststage.Candidate) map[string]float64 {
	scores := make(map[string]float64, len(candidates))
	for _, candidate := range candidates {
		scores[candidate.Code] = candidate.BlendScore
	}
	return percentilesByCode(scores)
}

func candidateGroup(candidate firststage.Candidate, index int) int {
	if candidate.Rank > 0 {
		return (candidate.Rank - 1) / JEVGroupSize
	}
	return index / JEVGroupSize
}

func baseReport(opts Options) Report {
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	return Report{
		Version:        ReportSchemaVersion,
		Status:         StatusOK,
		AsOf:           opts.AsOf,
		Target:         opts.Target,
		GeneratedAt:    now.In(market.Shanghai),
		Ranker:         RankerBlendWeighted,
		FeatureVersion: FeatureVersion,
		TopN:           opts.TopN,
		PushEnabled:    opts.PushEnabled,
		Candidates:     []Candidate{},
		Excluded:       []Excluded{},
		Rules: []string{
			"目标：未来3个交易日最高价相对信号日收盘的最大涨幅，是排序标签，不是可成交收益。",
			"候选只来自第一阶段高召回池；排名按计划权重融合第一阶段综合分池内百分位与离线模型组内百分位。",
			"离线模型只提供分数，生产进程不启动 Python、子进程或外部推理服务。",
			"树模型预测保留为辅助字段，不参与 Top10 排序。",
			"输出 Top10 仅为研究候选，不改变正式推荐、风险门槛或交易建议。",
		},
	}
}

func fail(report Report, err error) (Report, error) {
	report.Status = StatusFailed
	report.Reason = err.Error()
	report.Candidates = []Candidate{}
	return report, err
}

// LoadModel reads one offline model artifact. A missing file is a normal
// degraded input; malformed JSON, a stale date, or invalid scores are invalid.
func LoadModel(source ModelSource, asOf string) ModelInput {
	name := source.Name
	if name == "" {
		name = ModelJEV
	}
	input := ModelInput{Name: name, Status: ModelStatusNil}
	if strings.TrimSpace(source.Path) == "" {
		return input
	}
	data, err := os.ReadFile(source.Path)
	if err != nil {
		return input
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var doc JEVDocument
	if err := decoder.Decode(&doc); err != nil {
		input.Status = ModelStatusBad
		return input
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		input.Status = ModelStatusBad
		return input
	}
	if doc.SchemaVersion != 1 || doc.AsOf != asOf || len(doc.Scores) == 0 {
		input.Status = ModelStatusBad
		return input
	}
	for _, score := range doc.Scores {
		if !finite(score.Adjacent15) || !finite(score.Full15Top12) || score.Adjacent15 < 0 || score.Full15Top12 < 0 || score.Adjacent15 > 1 || score.Full15Top12 > 1 {
			input.Status = ModelStatusBad
			return input
		}
	}
	input.Status = ModelStatusOK
	input.Scores = doc.Scores
	return input
}

func LoadModels(sources []ModelSource, asOf string) []ModelInput {
	inputs := make([]ModelInput, 0, len(sources))
	for _, source := range sources {
		inputs = append(inputs, LoadModel(source, asOf))
	}
	return inputs
}

func loadJEV(path, asOf string) (map[string]JEVScore, string) {
	input := LoadModel(ModelSource{Name: ModelJEV, Path: path}, asOf)
	return input.Scores, input.Status
}

// LoadJEV reads a JEV artifact through the compatibility wrapper. Callers that
// support multiple models should use LoadModel or LoadModels instead.
func LoadJEV(path, asOf string) (map[string]JEVScore, string) {
	scores, status := loadJEV(path, asOf)
	return scores, status
}
