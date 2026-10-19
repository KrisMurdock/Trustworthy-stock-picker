package diagnostics

import (
	"encoding/json"
	"stock-watcher/internal/config"
	"stock-watcher/internal/selection"
	"stock-watcher/internal/strategy"
	"strings"
	"testing"
)

func fixture() selection.Report {
	return selection.Report{Version: 2, ID: "saved-id", Ranking: []strategy.Candidate{{Code: "sh600900", Rules: []string{"baseline", "momentum"}, Risks: []string{"historical_net_loss_limit_breached", "insufficient_or_unprofitable_evidence", "holdout_validation_not_passed"}}}, Research: &strategy.Research{Parameters: config.DefaultStrategy(), Holdout: strategy.Evaluation{Method: "frozen_holdout", SignalDates: 40}, Rules: []strategy.Stats{
		{Rule: "baseline", Target: 3, Horizon: 5, HitRate: .4},
		{Rule: "momentum", Target: 3, Horizon: 5, Samples: 1095, Dates: 154, Resolved: 1041, Unresolved: 54, MeanNet: .36, HitRate: .47, WorstNet: -15.3766, LossBreaches: 209, LossLimitPct: 3},
	}}}
}

func TestSavedReportDiagnosisExplainsFailuresWithoutChangingEvidence(t *testing.T) {
	r := fixture()
	before, _ := json.Marshal(r)
	d := Explain(r)
	after, _ := json.Marshal(r)
	if string(before) != string(after) {
		t.Fatal("diagnosis mutated the stored report")
	}
	c := d.Candidates["sh600900"]
	if c.Status != "failed" || len(c.Rules) != 1 || len(c.Rules[0].Failures) != 2 {
		t.Fatalf("wrong failure attribution: %+v", c)
	}
	f := c.Rules[0].Failures[0]
	if f.Code != "loss_limit" || f.Actual != -15.3766 || f.Required != -3 || f.Comparison != ">" || !strings.Contains(f.Message, "209笔") {
		t.Fatalf("lost measured/required values: %+v", f)
	}
	if d.Validation.Status != "no_recommendations" {
		t.Fatal("zero samples claimed a measured validation failure")
	}
	for _, f := range d.Validation.Failures {
		if strings.Contains(f.Message, "0.00%") {
			t.Fatal("zero samples shown as 0% performance")
		}
	}
	if strings.Contains(strings.Join(c.Risks, "/"), "独立验证") {
		t.Fatal("global validation repeated as per-stock evidence")
	}
}

func TestDiagnosticCompatibilityAndMissingData(t *testing.T) {
	if Explain(selection.Report{Version: 1}) != nil {
		t.Fatal("trend report acquired invented diagnostics")
	}
	for _, mode := range []string{"no_signal", "missing_rules", "missing_baseline", "missing_parameters", "legacy_loss_count", "no_holdout", "no_exits", "passed"} {
		t.Run(mode, func(t *testing.T) {
			r := fixture()
			switch mode {
			case "no_signal":
				r.Ranking[0].Rules = []string{"baseline"}
			case "missing_rules":
				r.Research.Rules = r.Research.Rules[:1]
			case "missing_baseline":
				r.Research.Rules = r.Research.Rules[1:]
			case "missing_parameters":
				r.Research.Parameters = config.Strategy{}
			case "legacy_loss_count":
				r.Research.Rules[1].LossBreaches = 0
				r.Research.Rules[1].LossLimitPct = 0
			case "no_holdout":
				r.Research.Holdout = strategy.Evaluation{}
			case "no_exits":
				r.Research.Holdout.Recommended = strategy.Stats{Samples: 30, Dates: 10, Unresolved: 30}
			case "passed":
				r.Research.ValidationPassed = true
			}
			d := Explain(r)
			c := d.Candidates["sh600900"]
			switch mode {
			case "no_signal":
				if c.Status != "no_signal" || len(c.Rules) != 0 {
					t.Fatal(c)
				}
			case "missing_rules", "missing_baseline", "missing_parameters":
				if c.Status != "unavailable" {
					t.Fatal("missing data treated as measured failure")
				}
			case "legacy_loss_count":
				if !strings.Contains(c.Rules[0].Failures[0].Message, "至少1笔") {
					t.Fatal("invented legacy count")
				}
			case "no_holdout":
				if d.Validation.Status != "unavailable" {
					t.Fatal(d.Validation)
				}
			case "no_exits":
				for _, f := range d.Validation.Failures {
					if f.Code == "mean_net" || f.Code == "baseline_mean_net" {
						t.Fatal("unknown exit return treated as zero")
					}
				}
			case "passed":
				if d.Validation.Status != "passed" || len(d.Validation.Failures) != 0 {
					t.Fatal("changed saved verdict")
				}
			}
		})
	}
}
