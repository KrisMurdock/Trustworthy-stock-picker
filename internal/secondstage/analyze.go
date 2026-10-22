package secondstage

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/firststage"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

// ValidateJEVInput guards a frozen snapshot supplied by the framework. It
// mirrors the artifact checks so neither a malformed caller nor a corrupt
// file can reach feature construction.
func ValidateJEVInput(in *JEVInput) error {
	if in == nil {
		return nil
	}
	switch in.Status {
	case JEVStatusOK:
		if len(in.Scores) == 0 {
			return errors.New("second-stage JEV snapshot is empty")
		}
	case JEVStatusNone, JEVStatusBad:
		if len(in.Scores) > 0 {
			return errors.New("second-stage JEV snapshot carries scores without an ok status")
		}
		return nil
	default:
		return errors.New("second-stage JEV snapshot has an unknown status")
	}
	for code, score := range in.Scores {
		if strings.TrimSpace(code) == "" || !finite(score.Adjacent15) || !finite(score.Full15Top12) || score.Adjacent15 < 0 || score.Full15Top12 < 0 || score.Adjacent15 > 1 || score.Full15Top12 > 1 {
			return fmt.Errorf("second-stage JEV score for %q is invalid", code)
		}
	}
	return nil
}

func Analyze(first firststage.Report, opts Options) (Report, error) {
	report := baseReport(opts)
	if err := ValidateJEVInput(opts.JEV); err != nil {
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
	report.ModelID = model.ModelID
	report.ModelSHA256 = model.SHA256
	report.TrainedFrom = model.TrainFrom
	report.TrainedThrough = model.TrainThrough
	report.InputCount = len(first.Candidates)

	jev, jevStatus := map[string]JEVScore{}, JEVStatusNone
	if opts.JEV != nil {
		jevStatus = opts.JEV.Status
		if jevStatus == "" {
			jevStatus = JEVStatusNone
		}
		if jevStatus == JEVStatusOK && opts.JEV.Scores != nil {
			jev = opts.JEV.Scores
		}
	} else {
		jev, jevStatus = loadJEV(opts.JEVPath, opts.AsOf)
	}
	report.JEVStatus = jevStatus
	report.Degraded = jevStatus != JEVStatusOK
	if report.Degraded {
		report.Rules = append(report.Rules, "JEV artifact unavailable or stale; tree model uses neutral JEV features and marks the result degraded.")
	}

	poolReturn5 := 0.0
	for _, candidate := range first.Candidates {
		poolReturn5 += candidate.Features.Return5
	}
	poolReturn5 /= float64(len(first.Candidates))
	groupStats := buildJEVGroupStats(first.Candidates, jev)
	rankDenominator := len(first.Candidates) - 1
	if rankDenominator < 1 {
		rankDenominator = 1
	}

	rows := make([]Candidate, 0, len(first.Candidates))
	missingJEV := 0
	for index, candidate := range first.Candidates {
		score, present := jev[candidate.Code]
		group := index / JEVGroupSize
		if candidate.Rank > 0 {
			group = (candidate.Rank - 1) / JEVGroupSize
		}
		rankPosition := float64(index) / float64(rankDenominator)
		if candidate.Rank > 0 {
			rankPosition = float64(candidate.Rank-1) / float64(rankDenominator)
		}
		values := featureVector(candidate, score, present, groupStats[group], rankPosition, poolReturn5)
		prediction, err := model.Predict(values)
		if err != nil {
			return fail(report, fmt.Errorf("second-stage candidate %s: %w", candidate.Code, err))
		}
		row := Candidate{
			Code:             candidate.Code,
			Name:             candidate.Name,
			Sector:           candidate.Sector,
			FirstStageRank:   candidate.Rank,
			FirstStageBlend:  candidate.BlendScore,
			JEVAdjacent15:    score.Adjacent15,
			JEVFull15Top12:   score.Full15Top12,
			JEVPresent:       present,
			PredictedMaxHigh: prediction,
			Features:         candidate.Features,
		}
		if !present {
			missingJEV++
			row.Reasons = append(row.Reasons, "jev_missing")
		}
		rows = append(rows, row)
	}
	if jevStatus == JEVStatusOK && missingJEV > 0 {
		report.Degraded = true
		report.Rules = append(report.Rules, "JEV artifact does not cover every first-stage candidate; uncovered rows use neutral JEV features.")
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].PredictedMaxHigh != rows[j].PredictedMaxHigh {
			return rows[i].PredictedMaxHigh > rows[j].PredictedMaxHigh
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
		FeatureVersion: FeatureVersion,
		TopN:           opts.TopN,
		PushEnabled:    opts.PushEnabled,
		Candidates:     []Candidate{},
		Excluded:       []Excluded{},
		Rules: []string{
			"目标：未来3个交易日最高价相对信号日收盘的最大涨幅，是排序标签，不是可成交收益。",
			"候选只来自第一阶段高召回池；树模型使用第一阶段排名、技术特征和离线 JEV 分数。",
			"JEV 只作为离线教师特征，生产进程不启动 Python、子进程或外部推理服务。",
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

func loadJEV(path, asOf string) (map[string]JEVScore, string) {
	if strings.TrimSpace(path) == "" {
		return map[string]JEVScore{}, JEVStatusNone
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]JEVScore{}, JEVStatusNone
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var doc JEVDocument
	if err := decoder.Decode(&doc); err != nil {
		return map[string]JEVScore{}, JEVStatusBad
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return map[string]JEVScore{}, JEVStatusBad
	}
	if doc.SchemaVersion != 1 || doc.AsOf != asOf || len(doc.Scores) == 0 {
		return map[string]JEVScore{}, JEVStatusBad
	}
	for _, score := range doc.Scores {
		if !finite(score.Adjacent15) || !finite(score.Full15Top12) || score.Adjacent15 < 0 || score.Full15Top12 < 0 || score.Adjacent15 > 1 || score.Full15Top12 > 1 {
			return map[string]JEVScore{}, JEVStatusBad
		}
	}
	return doc.Scores, JEVStatusOK
}

// LoadJEV reads and validates one daily artifact for the framework boundary.
// Callers pass the resulting scores into selection.Input so the algorithm does
// not need filesystem paths or data-directory access.
func LoadJEV(path, asOf string) (map[string]JEVScore, string) {
	scores, status := loadJEV(path, asOf)
	return scores, status
}
