package config

import (
	"errors"
	"time"
)

// DayRange configures the experimental premarket high/low forecast. It is
// deliberately separate from selection and notifications: a forecast is an
// auditable observation, never a recommendation or an executable order.
type DayRange struct {
	Enabled           bool   `yaml:"enabled" json:"enabled"`
	PushEnabled       bool   `yaml:"push_enabled" json:"push_enabled"`
	TrainAt           string `yaml:"train_at" json:"train_at"`
	PredictAt         string `yaml:"predict_at" json:"predict_at"`
	ReconcileAt       string `yaml:"reconcile_at" json:"reconcile_at"`
	HistoryDays       int    `yaml:"history_days" json:"history_days"`
	MinTrainingDays   int    `yaml:"min_training_days" json:"min_training_days"`
	CalibrationDays   int    `yaml:"calibration_days" json:"calibration_days"`
	ValidationDays    int    `yaml:"validation_days" json:"validation_days"`
	KeepModels        int    `yaml:"keep_models" json:"keep_models"`
	UseStockFeatures  bool   `yaml:"use_stock_features" json:"use_stock_features"`
	UseMarketFeatures bool   `yaml:"use_market_features" json:"use_market_features"`
	UseSectorFeatures bool   `yaml:"use_sector_features" json:"use_sector_features"`
}

func DefaultDayRange() DayRange {
	return DayRange{
		Enabled:           false,
		PushEnabled:       true,
		TrainAt:           "15:40",
		PredictAt:         "08:45",
		ReconcileAt:       "18:35",
		HistoryDays:       365,
		MinTrainingDays:   120,
		CalibrationDays:   60,
		ValidationDays:    20,
		KeepModels:        10,
		UseStockFeatures:  true,
		UseMarketFeatures: true,
		UseSectorFeatures: true,
	}
}

func (d DayRange) Validate() error {
	if !d.Enabled {
		return nil
	}
	for name, value := range map[string]string{
		"train_at": d.TrainAt, "predict_at": d.PredictAt, "reconcile_at": d.ReconcileAt,
	} {
		if _, err := time.Parse("15:04", value); err != nil || value == "" {
			return errors.New("dayrange." + name + " must be HH:MM")
		}
	}
	if d.TrainAt < "15:10" || d.TrainAt > "23:00" {
		return errors.New("dayrange.train_at must be between 15:10 and 23:00")
	}
	if d.PredictAt >= "09:30" {
		return errors.New("dayrange.predict_at must precede 09:30")
	}
	if d.ReconcileAt < "15:10" || d.ReconcileAt > "23:59" {
		return errors.New("dayrange.reconcile_at must be between 15:10 and 23:59")
	}
	if d.HistoryDays < 180 || d.HistoryDays > 7300 {
		return errors.New("dayrange.history_days must be 180 to 7300 calendar days")
	}
	if d.MinTrainingDays < 60 || d.MinTrainingDays > d.HistoryDays {
		return errors.New("dayrange.min_training_days must be between 60 and history_days")
	}
	if d.CalibrationDays < 10 || d.CalibrationDays > 250 {
		return errors.New("dayrange.calibration_days must be between 10 and 250")
	}
	if d.ValidationDays < 10 || d.ValidationDays > 120 {
		return errors.New("dayrange.validation_days must be between 10 and 120")
	}
	if d.KeepModels < 1 || d.KeepModels > 100 {
		return errors.New("dayrange.keep_models must be between 1 and 100")
	}
	if !d.UseStockFeatures && !d.UseMarketFeatures && !d.UseSectorFeatures {
		return errors.New("dayrange must enable at least one feature layer")
	}
	return nil
}
