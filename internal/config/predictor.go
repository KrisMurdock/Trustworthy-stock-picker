package config

import (
	"errors"
	"time"
)

// Predictor configures the experimental five-minute price forecaster. It is
// deliberately separate from Strategy: predictions are advisory observations,
// not executable trade plans.
type Predictor struct {
	Enabled         bool   `yaml:"enabled" json:"enabled"`
	PushEnabled     bool   `yaml:"push_enabled" json:"push_enabled"`
	TrainAt         string `yaml:"train_at" json:"train_at"`
	WriteMode       string `yaml:"write_mode" json:"write_mode"`
	MinTrainingDays int    `yaml:"min_training_days" json:"min_training_days"`
	ValidationDays  int    `yaml:"validation_days" json:"validation_days"`
	KeepModels      int    `yaml:"keep_models" json:"keep_models"`
}

func DefaultPredictor() Predictor {
	return Predictor{
		Enabled:         false,
		PushEnabled:     false,
		TrainAt:         "15:30",
		WriteMode:       "loopback",
		MinTrainingDays: 5,
		ValidationDays:  3,
		KeepModels:      10,
	}
}

func (p Predictor) Validate() error {
	if p.PushEnabled && !p.Enabled {
		return errors.New("predictor.push_enabled requires predictor.enabled")
	}
	if !p.Enabled {
		return nil
	}
	at, err := time.Parse("15:04", p.TrainAt)
	if err != nil || at.Format("15:04") != p.TrainAt || p.TrainAt < "15:10" || p.TrainAt > "23:00" {
		return errors.New("predictor.train_at must be between 15:10 and 23:00")
	}
	switch p.WriteMode {
	case "off", "loopback", "all":
	default:
		return errors.New("predictor.write_mode must be off, loopback or all")
	}
	if p.MinTrainingDays < 1 || p.MinTrainingDays > 250 {
		return errors.New("predictor.min_training_days must be between 1 and 250")
	}
	if p.ValidationDays < 1 || p.ValidationDays > 60 || p.ValidationDays >= p.MinTrainingDays+1 {
		return errors.New("predictor.validation_days must be between 1 and min_training_days")
	}
	if p.KeepModels < 1 || p.KeepModels > 100 {
		return errors.New("predictor.keep_models must be between 1 and 100")
	}
	return nil
}
