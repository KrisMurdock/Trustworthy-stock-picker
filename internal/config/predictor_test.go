package config

import (
	"strings"
	"testing"
)

func TestDefaultPredictorIsDisabledAndValid(t *testing.T) {
	p := DefaultPredictor()
	if p.Enabled || p.PushEnabled {
		t.Fatalf("default predictor must be opt-in: %+v", p)
	}
	if p.TrainAt != "15:30" || p.WriteMode != "loopback" || p.MinTrainingDays != 5 || p.ValidationDays != 3 || p.KeepModels != 10 {
		t.Fatalf("unexpected defaults: %+v", p)
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	p.Enabled = true
	p.PushEnabled = true
	if err := p.Validate(); err != nil {
		t.Fatalf("enabled defaults rejected: %v", err)
	}
}

func TestPredictorValidationRejectsUnsafeRanges(t *testing.T) {
	base := DefaultPredictor()
	base.Enabled = true
	cases := []struct {
		name   string
		mutate func(*Predictor)
		want   string
	}{
		{"push without enabled", func(p *Predictor) { p.Enabled = false; p.PushEnabled = true }, "push_enabled requires"},
		{"train time before close", func(p *Predictor) { p.TrainAt = "15:05" }, "train_at"},
		{"train time after limit", func(p *Predictor) { p.TrainAt = "23:01" }, "train_at"},
		{"train time malformed", func(p *Predictor) { p.TrainAt = "3:30" }, "train_at"},
		{"write mode", func(p *Predictor) { p.WriteMode = "public" }, "write_mode"},
		{"min days low", func(p *Predictor) { p.MinTrainingDays = 0 }, "min_training_days"},
		{"min days high", func(p *Predictor) { p.MinTrainingDays = 251 }, "min_training_days"},
		{"validation days zero", func(p *Predictor) { p.ValidationDays = 0 }, "validation_days"},
		{"validation days exceeds training", func(p *Predictor) { p.ValidationDays = p.MinTrainingDays + 1 }, "validation_days"},
		{"keep models zero", func(p *Predictor) { p.KeepModels = 0 }, "keep_models"},
		{"keep models high", func(p *Predictor) { p.KeepModels = 101 }, "keep_models"},
	}
	for _, tc := range cases {
		p := base
		tc.mutate(&p)
		err := p.Validate()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: got %v", tc.name, err)
		}
	}
}
