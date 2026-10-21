package config

import "testing"

func TestDayRangeEnvironmentAndValidation(t *testing.T) {
	c := Defaults()
	t.Setenv("STOCK_WATCHER_DAYRANGE_ENABLED", "true")
	t.Setenv("STOCK_WATCHER_DAYRANGE_TRAIN_AT", "16:20")
	t.Setenv("STOCK_WATCHER_DAYRANGE_PREDICT_AT", "08:30")
	t.Setenv("STOCK_WATCHER_DAYRANGE_RECONCILE_AT", "19:10")
	t.Setenv("STOCK_WATCHER_DAYRANGE_HISTORY_DAYS", "500")
	t.Setenv("STOCK_WATCHER_DAYRANGE_MIN_TRAIN_DAYS", "180")
	t.Setenv("STOCK_WATCHER_DAYRANGE_CALIBRATION_DAYS", "45")
	t.Setenv("STOCK_WATCHER_DAYRANGE_VALIDATION_DAYS", "30")
	t.Setenv("STOCK_WATCHER_DAYRANGE_KEEP_MODELS", "6")
	t.Setenv("STOCK_WATCHER_DAYRANGE_STOCK_FEATURES", "true")
	t.Setenv("STOCK_WATCHER_DAYRANGE_MARKET_FEATURES", "false")
	t.Setenv("STOCK_WATCHER_DAYRANGE_SECTOR_FEATURES", "true")
	if err := applyEnvironment(&c); err != nil {
		t.Fatal(err)
	}
	if !c.DayRange.Enabled || c.DayRange.TrainAt != "16:20" || c.DayRange.PredictAt != "08:30" || c.DayRange.ReconcileAt != "19:10" ||
		c.DayRange.HistoryDays != 500 || c.DayRange.MinTrainingDays != 180 || c.DayRange.CalibrationDays != 45 ||
		c.DayRange.ValidationDays != 30 || c.DayRange.KeepModels != 6 || c.DayRange.UseMarketFeatures {
		t.Fatalf("dayrange environment was not applied: %+v", c.DayRange)
	}
	if err := c.DayRange.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDayRangeRejectsUnsafeScheduleAndDisabledFeatureLayers(t *testing.T) {
	c := DefaultDayRange()
	c.Enabled = true
	c.PredictAt = "09:30"
	if err := c.Validate(); err == nil {
		t.Fatal("dayrange accepted a prediction cut-off at the market open")
	}
	c = DefaultDayRange()
	c.Enabled = true
	c.UseStockFeatures, c.UseMarketFeatures, c.UseSectorFeatures = false, false, false
	if err := c.Validate(); err == nil {
		t.Fatal("dayrange accepted a model without any feature layer")
	}
}
