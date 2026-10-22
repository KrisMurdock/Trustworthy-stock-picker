package secondstage

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/firststage"
)

func fixtureFirstStage(asOf, target string) firststage.Report {
	return firststage.Report{
		Version: firststage.ReportSchemaVersion,
		Status:  firststage.StatusOK,
		AsOf:    asOf,
		Target:  target,
		Candidates: []firststage.Candidate{
			{Rank: 1, Code: "sh600001", Name: "甲", BlendScore: 0.9, BaseScore: 80, ModelScore: 0.8, BaseRank: 0.9, ModelRank: 0.8, Features: firststage.FeatureSnapshot{Return5: 3}},
			{Rank: 2, Code: "sh600002", Name: "乙", BlendScore: 0.8, BaseScore: 70, ModelScore: 0.7, BaseRank: 0.7, ModelRank: 0.6, Features: firststage.FeatureSnapshot{Return5: 2}},
			{Rank: 3, Code: "sh600003", Name: "丙", BlendScore: 0.7, BaseScore: 60, ModelScore: 0.6, BaseRank: 0.5, ModelRank: 0.4, Features: firststage.FeatureSnapshot{Return5: 1}},
		},
	}
}

func writeJEV(t *testing.T, asOf string, scores map[string]JEVScore) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "jev.json")
	doc := JEVDocument{SchemaVersion: 1, AsOf: asOf, Scores: scores}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func loadedModel(t *testing.T, name, asOf, path string) ModelInput {
	t.Helper()
	return LoadModel(ModelSource{Name: name, Path: path}, asOf)
}

func rankingModel() Model {
	return Model{
		ModelID:       "test_tree",
		TrainFrom:     "2026-09-03",
		TrainThrough:  "2026-10-14",
		SHA256:        "test",
		FeatureNames:  append([]string(nil), FeatureNames...),
		Base:          0,
		LearningRate:  1,
		LabelClipLow:  -10,
		LabelClipHigh: 20,
		Trees: []Tree{{Nodes: []Node{
			{Feature: 5, Threshold: 0.5, Left: 1, Right: 2},
			{Leaf: true, Value: -1},
			{Leaf: true, Value: 1},
		}}},
	}
}

func TestAnalyzeRanksWithJEVAndClipsTopN(t *testing.T) {
	asOf, target := "2026-10-20", "2026-10-21"
	path := writeJEV(t, asOf, map[string]JEVScore{
		"sh600001": {Adjacent15: 0.2},
		"sh600002": {Adjacent15: 0.9},
		"sh600003": {Adjacent15: 0.4},
	})
	opts := Options{AsOf: asOf, Target: target, TopN: 2, PushEnabled: true, Models: []ModelInput{loadedModel(t, ModelJEV, asOf, path)}, Now: time.Unix(0, 0)}
	report, err := analyzeWithModel(baseReport(opts), fixtureFirstStage(asOf, target), opts, rankingModel())
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != StatusOK || report.JEVStatus != JEVStatusOK || report.Degraded || !report.PushEnabled {
		t.Fatalf("unexpected report status: %+v", report)
	}
	if report.Ranker != RankerBlendWeighted || report.Blend.ID != DefaultBlendID {
		t.Fatalf("unexpected ranker: %q", report.Ranker)
	}
	if len(report.Candidates) != 2 || report.Candidates[0].Code != "sh600002" || report.Candidates[0].Rank != 1 {
		t.Fatalf("unexpected top candidates: %+v", report.Candidates)
	}
	if !report.Candidates[0].JEVPresent || report.Candidates[0].JEVAdjacent15 != 0.9 {
		t.Fatalf("JEV evidence lost: %+v", report.Candidates[0])
	}
	if report.Candidates[0].FirstStagePercentile != 0.5 || report.Candidates[0].JEVPercentile != 1 || report.Candidates[0].BlendScore != 0.75 {
		t.Fatalf("unexpected blend inputs: %+v", report.Candidates[0])
	}
	if report.Candidates[1].BlendScore != 0.5 {
		t.Fatalf("unexpected second blend score: %+v", report.Candidates[1])
	}
	if len(report.Excluded) != 1 || report.Excluded[0].Code != "sh600003" {
		t.Fatalf("unexpected exclusions: %+v", report.Excluded)
	}
}

func TestAnalyzeDegradesWhenJEVMissing(t *testing.T) {
	asOf, target := "2026-10-20", "2026-10-21"
	missingPath := filepath.Join(t.TempDir(), "missing.json")
	opts := Options{AsOf: asOf, Target: target, TopN: 3, Models: []ModelInput{loadedModel(t, ModelJEV, asOf, missingPath)}, Now: time.Unix(0, 0)}
	report, err := analyzeWithModel(baseReport(opts), fixtureFirstStage(asOf, target), opts, rankingModel())
	if err != nil {
		t.Fatal(err)
	}
	if !report.Degraded || report.JEVStatus != JEVStatusNone {
		t.Fatalf("missing JEV did not degrade: %+v", report)
	}
	if len(report.Candidates) != 3 {
		t.Fatalf("tree-only fallback lost candidates: %+v", report)
	}
	wantOrder := []string{"sh600001", "sh600002", "sh600003"}
	for index, candidate := range report.Candidates {
		if candidate.Code != wantOrder[index] {
			t.Fatalf("missing-JEV fallback changed first-stage order: %+v", report.Candidates)
		}
		if candidate.JEVPresent || !hasReason(candidate.Reasons, "jev_missing") {
			t.Fatalf("candidate did not mark missing JEV: %+v", candidate)
		}
		if candidate.JEVPercentile != 0.5 || candidate.BlendScore != 0.5*candidate.FirstStagePercentile+0.25 {
			t.Fatalf("missing-JEV blend did not use a neutral percentile: %+v", candidate)
		}
	}
}

func TestAnalyzeBlendsMultipleModels(t *testing.T) {
	asOf, target := "2026-10-20", "2026-10-21"
	plan := BlendPlan{
		ID:               "blend_60_20_20",
		FirstStageWeight: 0.4,
		Models: []BlendWeight{
			{Name: "jev", Weight: 0.3},
			{Name: "laya_official", Weight: 0.3},
		},
	}
	models := []ModelInput{
		{Name: "jev", Status: ModelStatusOK, Scores: map[string]ModelScore{
			"sh600001": {Adjacent15: 0.1}, "sh600002": {Adjacent15: 0.9}, "sh600003": {Adjacent15: 0.5},
		}},
		{Name: "laya_official", Status: ModelStatusOK, Scores: map[string]ModelScore{
			"sh600001": {Adjacent15: 0.5}, "sh600002": {Adjacent15: 0.7}, "sh600003": {Adjacent15: 0.9},
		}},
	}
	opts := Options{AsOf: asOf, Target: target, TopN: 3, Blend: plan, Models: models, Now: time.Unix(0, 0)}
	report, err := analyzeWithModel(baseReport(opts), fixtureFirstStage(asOf, target), opts, rankingModel())
	if err != nil {
		t.Fatal(err)
	}
	if report.Degraded || len(report.Models) != 2 {
		t.Fatalf("multi-model blend degraded: %+v", report)
	}
	want := []string{"sh600002", "sh600003", "sh600001"}
	wantScores := []float64{0.65, 0.45, 0.4}
	for index, code := range want {
		candidate := report.Candidates[index]
		if candidate.Code != code || math.Abs(candidate.BlendScore-wantScores[index]) > 1e-9 {
			t.Fatalf("unexpected multi-model order at %d: %+v", index, candidate)
		}
	}
	if report.Candidates[0].Models["laya_official"].Percentile != 0.5 {
		t.Fatalf("second model percentile missing: %+v", report.Candidates[0].Models)
	}
}

func TestAnalyzeLocalCalibrationAndATRRiskAdjustment(t *testing.T) {
	asOf, target := "2026-10-20", "2026-10-21"
	plan := BlendPlan{
		ID:               "risk_all20_local035_eta1",
		FirstStageWeight: 0.2,
		Models: []BlendWeight{
			{Name: "jev", Weight: 0.2},
			{Name: "laya_official", Weight: 0.2},
			{Name: "laya_multilingual", Weight: 0.2},
			{Name: "chinese_decision", Weight: 0.2},
		},
		GroupSize:        5,
		Calibration:      CalibrationLocal,
		CalibrationParam: 0.35,
		RiskEta:          1,
	}
	first := firststage.Report{
		Version: firststage.ReportSchemaVersion,
		Status:  firststage.StatusOK,
		AsOf:    asOf,
		Target:  target,
		Candidates: []firststage.Candidate{
			{Rank: 1, Code: "sh600001", BlendScore: 0.9, Features: firststage.FeatureSnapshot{ATR14Pct: 9}},
			{Rank: 2, Code: "sh600002", BlendScore: 0.8, Features: firststage.FeatureSnapshot{ATR14Pct: 1}},
			{Rank: 3, Code: "sh600003", BlendScore: 0.7, Features: firststage.FeatureSnapshot{ATR14Pct: 1}},
			{Rank: 4, Code: "sh600004", BlendScore: 0.6, Features: firststage.FeatureSnapshot{ATR14Pct: 1}},
			{Rank: 5, Code: "sh600005", BlendScore: 0.5, Features: firststage.FeatureSnapshot{ATR14Pct: 1}},
		},
	}
	models := make([]ModelInput, 0, 4)
	for _, name := range []string{"jev", "laya_official", "laya_multilingual", "chinese_decision"} {
		models = append(models, ModelInput{Name: name, Status: ModelStatusOK, Scores: map[string]ModelScore{
			"sh600001": {Adjacent15: 0.1},
			"sh600002": {Adjacent15: 0.2},
			"sh600003": {Adjacent15: 0.3},
			"sh600004": {Adjacent15: 0.4},
			"sh600005": {Adjacent15: 0.5},
		}})
	}
	opts := Options{AsOf: asOf, Target: target, TopN: 5, Blend: plan, Models: models, Now: time.Unix(0, 0)}
	report, err := analyzeWithModel(baseReport(opts), first, opts, rankingModel())
	if err != nil {
		t.Fatal(err)
	}
	if report.Degraded || len(report.Models) != 4 {
		t.Fatalf("complete five-signal blend degraded: %+v", report)
	}
	wantOrder := []string{"sh600005", "sh600004", "sh600003", "sh600002", "sh600001"}
	for index, code := range wantOrder {
		if report.Candidates[index].Code != code {
			t.Fatalf("risk-adjusted order at %d = %s want %s: %+v", index, report.Candidates[index].Code, code, report.Candidates)
		}
	}
	best := report.Candidates[0]
	if math.Abs(best.WeightedScore-0.54) > 1e-12 || math.Abs(best.ATRPercentile-0.375) > 1e-12 || math.Abs(best.RiskPenalty-0.375) > 1e-12 || math.Abs(best.BlendScore-0.165) > 1e-12 {
		t.Fatalf("unexpected risk-adjusted scores: %+v", best)
	}
	jev := best.Models["jev"]
	if math.Abs(jev.Percentile-1) > 1e-12 || math.Abs(jev.Calibrated-0.675) > 1e-12 {
		t.Fatalf("unexpected local calibration: %+v", jev)
	}
	worst := report.Candidates[len(report.Candidates)-1]
	if worst.Code != "sh600001" || math.Abs(worst.WeightedScore-0.46) > 1e-12 || math.Abs(worst.RiskPenalty-1) > 1e-12 || math.Abs(worst.BlendScore+0.54) > 1e-12 {
		t.Fatalf("unexpected high-ATR candidate: %+v", worst)
	}
}

func TestAnalyzeMissingModelRemainsNeutralWithLocalCalibration(t *testing.T) {
	asOf, target := "2026-10-20", "2026-10-21"
	plan := BlendPlan{
		ID:               "two_model_local",
		FirstStageWeight: 0.5,
		Models:           []BlendWeight{{Name: "jev", Weight: 0.25}, {Name: "laya_official", Weight: 0.25}},
		GroupSize:        5,
		Calibration:      CalibrationLocal,
		CalibrationParam: 0.35,
	}
	models := []ModelInput{
		{Name: "jev", Status: ModelStatusOK, Scores: map[string]ModelScore{
			"sh600001": {Adjacent15: 0.9}, "sh600002": {Adjacent15: 0.8}, "sh600003": {Adjacent15: 0.7},
		}},
		{Name: "laya_official", Status: ModelStatusNil},
	}
	opts := Options{AsOf: asOf, Target: target, TopN: 3, Blend: plan, Models: models}
	report, err := analyzeWithModel(baseReport(opts), fixtureFirstStage(asOf, target), opts, rankingModel())
	if err != nil {
		t.Fatal(err)
	}
	if !report.Degraded {
		t.Fatalf("missing model did not degrade: %+v", report)
	}
	for _, candidate := range report.Candidates {
		signal := candidate.Models["laya_official"]
		if signal.Present || signal.Calibrated != 0.5 || !hasReason(candidate.Reasons, "model_missing:laya_official") {
			t.Fatalf("missing model was not neutral: %+v", candidate)
		}
	}
}

func TestBlendPlanValidation(t *testing.T) {
	if err := DefaultBlendPlan().Validate(); err != nil {
		t.Fatalf("default blend plan rejected: %v", err)
	}
	for _, test := range []struct {
		name string
		plan BlendPlan
	}{
		{"weights do not sum", BlendPlan{ID: "bad", FirstStageWeight: 0.5, Models: []BlendWeight{{Name: "jev", Weight: 0.4}}}},
		{"zero model weight", BlendPlan{ID: "bad", FirstStageWeight: 0.5, Models: []BlendWeight{{Name: "jev", Weight: 0}}}},
		{"duplicate model", BlendPlan{ID: "bad", FirstStageWeight: 0.5, Models: []BlendWeight{{Name: "jev", Weight: 0.25}, {Name: "jev", Weight: 0.25}}}},
		{"invalid model name", BlendPlan{ID: "bad", FirstStageWeight: 0.5, Models: []BlendWeight{{Name: "../jev", Weight: 0.5}}}},
		{"invalid group size", BlendPlan{ID: "bad", FirstStageWeight: 1, Models: []BlendWeight{{Name: "jev", Weight: 0}}, GroupSize: 101}},
		{"invalid calibration", BlendPlan{ID: "bad", FirstStageWeight: 1, Models: []BlendWeight{{Name: "jev", Weight: 0}}, Calibration: "bayes"}},
		{"invalid calibration parameter", BlendPlan{ID: "bad", FirstStageWeight: 1, Models: []BlendWeight{{Name: "jev", Weight: 0}}, CalibrationParam: -0.1}},
		{"invalid risk eta", BlendPlan{ID: "bad", FirstStageWeight: 1, Models: []BlendWeight{{Name: "jev", Weight: 0}}, RiskEta: 1.1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.plan.Validate(); err == nil {
				t.Fatalf("invalid plan accepted: %+v", test.plan)
			}
		})
	}
}

func TestFirstStagePercentilesUseTieAveraging(t *testing.T) {
	candidates := []firststage.Candidate{
		{Rank: 1, Code: "sh600001", BlendScore: 0.9},
		{Rank: 2, Code: "sh600002", BlendScore: 0.9},
		{Rank: 3, Code: "sh600003", BlendScore: 0.5},
		{Rank: 4, Code: "sh600004", BlendScore: 0.1},
	}
	got := firstStagePercentilesByCode(candidates)
	want := map[string]float64{
		"sh600001": 5.0 / 6.0,
		"sh600002": 5.0 / 6.0,
		"sh600003": 1.0 / 3.0,
		"sh600004": 0,
	}
	for code, value := range want {
		if got[code] != value {
			t.Fatalf("first-stage percentile for %s = %v want %v", code, got[code], value)
		}
	}
	single := firstStagePercentilesByCode([]firststage.Candidate{{Code: "sh600001", BlendScore: 0.9}})
	if single["sh600001"] != 0.5 {
		t.Fatalf("single-candidate percentile = %v", single["sh600001"])
	}
}

func TestAnalyzePartialJEVCoverageUsesNeutralPercentile(t *testing.T) {
	asOf, target := "2026-10-20", "2026-10-21"
	path := writeJEV(t, asOf, map[string]JEVScore{
		"sh600001": {Adjacent15: 0.9},
		"sh600002": {Adjacent15: 0.1},
	})
	opts := Options{AsOf: asOf, Target: target, TopN: 3, Models: []ModelInput{loadedModel(t, ModelJEV, asOf, path)}}
	report, err := analyzeWithModel(baseReport(opts), fixtureFirstStage(asOf, target), opts, rankingModel())
	if err != nil {
		t.Fatal(err)
	}
	if !report.Degraded || report.JEVStatus != JEVStatusOK {
		t.Fatalf("partial JEV coverage did not degrade: %+v", report)
	}
	if len(report.Candidates) != 3 || report.Candidates[0].Code != "sh600001" {
		t.Fatalf("unexpected order: %+v", report.Candidates)
	}
	if report.Candidates[0].BlendScore != 1 {
		t.Fatalf("first candidate blend score: %+v", report.Candidates[0])
	}
	var missing *Candidate
	for index := range report.Candidates {
		if report.Candidates[index].Code == "sh600003" {
			missing = &report.Candidates[index]
		}
	}
	if missing == nil || missing.JEVPresent || missing.JEVPercentile != 0.5 || missing.BlendScore != 0.25 || !hasReason(missing.Reasons, "jev_missing") {
		t.Fatalf("missing JEV row was not neutralized: %+v", missing)
	}
}

func TestAnalyzeRejectsStaleOrEmptyInput(t *testing.T) {
	report, err := Analyze(firststage.Report{}, Options{AsOf: "2026-10-20", Target: "2026-10-21", TopN: 10})
	if err == nil || report.Status != StatusFailed {
		t.Fatalf("empty first-stage report accepted: %+v err=%v", report, err)
	}
	stale := fixtureFirstStage("2026-10-19", "2026-10-20")
	if _, err := Analyze(stale, Options{AsOf: "2026-10-20", Target: "2026-10-21", TopN: 10}); err == nil {
		t.Fatal("stale first-stage report accepted")
	}
}

func TestModelArtifactContract(t *testing.T) {
	model, err := parseModel(modelArtifact, "")
	if err != nil {
		t.Fatal(err)
	}
	if model.FeatureVersion != FeatureVersion || len(model.Trees) == 0 {
		t.Fatalf("unexpected model: %+v", model)
	}
	if _, err := parseModel([]byte(strings.Replace(string(modelArtifact), `"jev_adjacent15"`, `"jev_unknown"`, 1)), ""); err == nil {
		t.Fatal("unknown feature order accepted")
	}
}

func TestInvalidJEVDocumentFallsBack(t *testing.T) {
	asOf, target := "2026-10-20", "2026-10-21"
	path := filepath.Join(t.TempDir(), "jev.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"as_of":"2026-10-19","scores":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := Options{AsOf: asOf, Target: target, TopN: 3, Models: []ModelInput{loadedModel(t, ModelJEV, asOf, path)}}
	report, err := analyzeWithModel(baseReport(opts), fixtureFirstStage(asOf, target), opts, rankingModel())
	if err != nil {
		t.Fatal(err)
	}
	if report.JEVStatus != JEVStatusBad || !report.Degraded {
		t.Fatalf("invalid JEV did not degrade: %+v", report)
	}
}

func TestLoadJEVRejectsMalformedScores(t *testing.T) {
	for _, body := range []string{
		`{"schema_version":1,"as_of":"2026-10-20","scores":{"sh600001":{"adjacent15":-0.1,"full15_top12":0.2}}}`,
		`{"schema_version":1,"as_of":"2026-10-20","scores":{"sh600001":{"adjacent15":1.1,"full15_top12":0.2}}}`,
		`{"schema_version":1,"as_of":"2026-10-20","scores":{"sh600001":{"adjacent15":0.1,"full15_top12":0.2}}} {}`,
	} {
		path := filepath.Join(t.TempDir(), "jev.json")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, status := loadJEV(path, "2026-10-20"); status != JEVStatusBad {
			t.Fatalf("malformed JEV accepted: %s status=%s", body, status)
		}
	}
}

func TestEmbeddedModelChecksumIsPinned(t *testing.T) {
	if modelArtifactSHA256 == "" || modelArtifactSHA256 == "REPLACE_WITH_MODEL_SHA256" {
		t.Fatal("embedded second-stage model checksum is not pinned")
	}
	model, err := DefaultModel()
	if err != nil {
		t.Fatal(err)
	}
	if model.SHA256 != modelArtifactSHA256 || len(model.Trees) == 0 {
		t.Fatalf("unexpected embedded model: %+v", model)
	}
}

func TestValidateModelInputsRejectsMalformedSnapshot(t *testing.T) {
	for _, input := range []*JEVInput{
		{Status: JEVStatusOK},
		{Status: "unknown"},
		{Status: JEVStatusOK, Scores: map[string]JEVScore{"sh600001": {Adjacent15: 1.2}}},
		{Status: JEVStatusNone, Scores: map[string]JEVScore{"sh600001": {Adjacent15: 0.2}}},
	} {
		if err := ValidateJEVInput(input); err == nil {
			t.Fatalf("malformed JEV snapshot accepted: %+v", input)
		}
	}
	if err := ValidateJEVInput(&JEVInput{Status: JEVStatusNone}); err != nil {
		t.Fatalf("missing JEV snapshot rejected: %v", err)
	}
	if err := ValidateModelInputs([]ModelInput{{Name: "jev", Status: ModelStatusNil}, {Name: "jev", Status: ModelStatusNil}}); err == nil {
		t.Fatal("duplicate model snapshots accepted")
	}
}

func hasReason(reasons []string, want string) bool {
	for _, reason := range reasons {
		if reason == want {
			return true
		}
	}
	return false
}
