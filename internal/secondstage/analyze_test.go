package secondstage

import (
	"encoding/json"
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
	opts := Options{AsOf: asOf, Target: target, TopN: 2, JEVPath: path, Now: time.Unix(0, 0)}
	report, err := analyzeWithModel(baseReport(opts), fixtureFirstStage(asOf, target), opts, rankingModel())
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != StatusOK || report.JEVStatus != JEVStatusOK || report.Degraded {
		t.Fatalf("unexpected report status: %+v", report)
	}
	if len(report.Candidates) != 2 || report.Candidates[0].Code != "sh600002" || report.Candidates[0].Rank != 1 {
		t.Fatalf("unexpected top candidates: %+v", report.Candidates)
	}
	if !report.Candidates[0].JEVPresent || report.Candidates[0].JEVAdjacent15 != 0.9 {
		t.Fatalf("JEV evidence lost: %+v", report.Candidates[0])
	}
	if len(report.Excluded) != 1 || report.Excluded[0].Code != "sh600003" {
		t.Fatalf("unexpected exclusions: %+v", report.Excluded)
	}
}

func TestAnalyzeDegradesWhenJEVMissing(t *testing.T) {
	asOf, target := "2026-10-20", "2026-10-21"
	opts := Options{AsOf: asOf, Target: target, TopN: 3, JEVPath: filepath.Join(t.TempDir(), "missing.json"), Now: time.Unix(0, 0)}
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
	for _, candidate := range report.Candidates {
		if candidate.JEVPresent || !hasReason(candidate.Reasons, "jev_missing") {
			t.Fatalf("candidate did not mark missing JEV: %+v", candidate)
		}
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
	opts := Options{AsOf: asOf, Target: target, TopN: 3, JEVPath: path}
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

func TestValidateJEVInputRejectsMalformedSnapshot(t *testing.T) {
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
}

func hasReason(reasons []string, want string) bool {
	for _, reason := range reasons {
		if reason == want {
			return true
		}
	}
	return false
}
