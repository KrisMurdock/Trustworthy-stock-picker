package config

import (
	"strings"
	"testing"
)

func TestSecondStageDefaultsAndBuiltin(t *testing.T) {
	second := DefaultSecondStage()
	if second.Enabled || second.PushEnabled || second.TopN != 10 || second.BlendID != "blend_50_50" || second.FirstStageWeight != 0.5 || len(second.Models) != 1 || second.Models[0] != (ModelWeight{Name: "jev", Weight: 0.5}) || second.GroupSize != 15 || second.Calibration != "none" || second.CalibrationParam != 0 || second.RiskEta != 0 {
		t.Fatalf("unexpected defaults: %+v", second)
	}
	builtin := Builtin()
	if !builtin.SecondStage.Enabled || !builtin.SecondStage.PushEnabled || builtin.SecondStage.TopN != 10 || builtin.SecondStage.BlendID != "risk_all20_local035_eta1" || builtin.SecondStage.FirstStageWeight != 0.2 || len(builtin.SecondStage.Models) != 4 || builtin.SecondStage.GroupSize != 5 || builtin.SecondStage.Calibration != "local" || builtin.SecondStage.CalibrationParam != 0.35 || builtin.SecondStage.RiskEta != 1 {
		t.Fatalf("production second-stage is not enabled: %+v", builtin.SecondStage)
	}
}

func TestSecondStageValidation(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*SecondStage)
		want   string
	}{
		{"top n zero", func(second *SecondStage) { second.Enabled = true; second.TopN = 0 }, "top_n"},
		{"top n too large", func(second *SecondStage) { second.Enabled = true; second.TopN = 101 }, "top_n"},
		{"push without enabled", func(second *SecondStage) { second.Enabled = false; second.PushEnabled = true }, "push_enabled requires"},
		{"bad blend id", func(second *SecondStage) { second.Enabled = true; second.BlendID = "bad/id" }, "blend_id"},
		{"bad group size", func(second *SecondStage) { second.Enabled = true; second.GroupSize = 0 }, "group_size"},
		{"bad calibration", func(second *SecondStage) { second.Enabled = true; second.Calibration = "bayes" }, "calibration"},
		{"bad calibration parameter", func(second *SecondStage) { second.Enabled = true; second.CalibrationParam = 1.1 }, "calibration_param"},
		{"bad risk eta", func(second *SecondStage) { second.Enabled = true; second.RiskEta = 1.1 }, "risk_eta"},
		{"weights do not sum", func(second *SecondStage) { second.Enabled = true; second.Models[0].Weight = 0.4 }, "sum to 1"},
		{"duplicate models", func(second *SecondStage) {
			second.Enabled = true
			second.Models = append(second.Models, ModelWeight{Name: "jev", Weight: 0})
		}, "duplicated"},
	} {
		t.Run(test.name, func(t *testing.T) {
			second := DefaultSecondStage()
			test.mutate(&second)
			if err := second.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
	c := Builtin()
	c.FirstStage.Enabled = false
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "requires first_stage.enabled") {
		t.Fatalf("second-stage without first-stage accepted: %v", err)
	}
}

func TestSecondStageEnvironmentOverrides(t *testing.T) {
	t.Setenv("STOCK_WATCHER_SECOND_STAGE_ENABLED", "false")
	t.Setenv("STOCK_WATCHER_SECOND_STAGE_PUSH_ENABLED", "false")
	t.Setenv("STOCK_WATCHER_SECOND_STAGE_TOP_N", "7")
	t.Setenv("STOCK_WATCHER_SECOND_STAGE_BLEND_ID", "blend_jev_laya")
	t.Setenv("STOCK_WATCHER_SECOND_STAGE_FIRST_STAGE_WEIGHT", "0.5")
	t.Setenv("STOCK_WATCHER_SECOND_STAGE_MODELS", "jev=0.25,laya_official=0.25")
	t.Setenv("STOCK_WATCHER_SECOND_STAGE_GROUP_SIZE", "9")
	t.Setenv("STOCK_WATCHER_SECOND_STAGE_CALIBRATION", "local")
	t.Setenv("STOCK_WATCHER_SECOND_STAGE_CALIBRATION_PARAM", "0.25")
	t.Setenv("STOCK_WATCHER_SECOND_STAGE_RISK_ETA", "0.5")
	c := Builtin()
	if err := applyEnvironment(&c); err != nil {
		t.Fatal(err)
	}
	if c.SecondStage.Enabled || c.SecondStage.PushEnabled || c.SecondStage.TopN != 7 || c.SecondStage.BlendID != "blend_jev_laya" || len(c.SecondStage.Models) != 2 || c.SecondStage.Models[1] != (ModelWeight{Name: "laya_official", Weight: 0.25}) || c.SecondStage.GroupSize != 9 || c.SecondStage.Calibration != "local" || c.SecondStage.CalibrationParam != 0.25 || c.SecondStage.RiskEta != 0.5 {
		t.Fatalf("environment did not override second-stage: %+v", c.SecondStage)
	}
}
