package health

import (
	"errors"
	"io/fs"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/intraday"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/predictor"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

const predictorStaleAfter = 15 * time.Minute

// PredictorStatus is the read-only predictor state exposed by health probes.
type PredictorStatus struct {
	Enabled           bool       `json:"enabled"`
	PushEnabled       bool       `json:"push_enabled"`
	ModelID           string     `json:"model_id,omitempty"`
	ModelValidFor     string     `json:"model_valid_for,omitempty"`
	LastPredictionAt  *time.Time `json:"last_prediction_at,omitempty"`
	PredictionsToday  int        `json:"predictions_today"`
	PushDelivered     int        `json:"push_delivered"`
	PushBlocked       int        `json:"push_blocked"`
	LastError         string     `json:"last_error,omitempty"`
	StatusUpdatedAt   *time.Time `json:"updated_at,omitempty"`
	StatusFilePresent bool       `json:"status_file_present"`
}

func predictorHealth(view *storage.Store, c config.Config, cal *calendar.Calendar, now time.Time) (*PredictorStatus, []string) {
	if !c.Predictor.Enabled {
		return nil, nil
	}
	status, err := predictor.ReadStatus(view)
	if err != nil {
		issues := []string{}
		if errors.Is(err, fs.ErrNotExist) {
			issues = append(issues, "predictor_status_missing")
		} else {
			issues = append(issues, "predictor_status_invalid")
		}
		return &PredictorStatus{Enabled: true, PushEnabled: c.Predictor.PushEnabled}, issues
	}
	out := &PredictorStatus{
		Enabled:           true,
		PushEnabled:       status.PushEnabled,
		ModelID:           status.ModelID,
		ModelValidFor:     status.ModelValidFor,
		PredictionsToday:  status.PredictionsToday,
		PushDelivered:     status.PushDelivered,
		PushBlocked:       status.PushBlocked,
		LastError:         status.LastError,
		StatusFilePresent: true,
	}
	if !status.LastPredictionAt.IsZero() {
		at := status.LastPredictionAt
		out.LastPredictionAt = &at
	}
	if !status.UpdatedAt.IsZero() {
		at := status.UpdatedAt
		out.StatusUpdatedAt = &at
	}
	return out, predictorIssues(status, c, cal, now)
}

func predictorIssues(status predictor.Status, c config.Config, cal *calendar.Calendar, now time.Time) []string {
	issues := []string{}
	if status.LastError != "" {
		issues = append(issues, "predictor_error")
	}
	if status.ModelID == "" {
		issues = append(issues, "predictor_model_missing")
	}
	plan, err := intraday.Schedule(cal, now, c.Intraday.CalibrationAt)
	if err == nil && plan.Active {
		if status.ModelID != "" && status.ModelValidFor != calendar.Date(now) {
			issues = append(issues, "predictor_model_stale")
		}
		if predictionDue(now) && (status.LastPredictionAt.IsZero() || now.Sub(status.LastPredictionAt) > predictorStaleAfter) {
			issues = append(issues, "predictor_prediction_stale")
		}
	}
	if status.PushBlocked > 0 {
		issues = append(issues, "predictor_push_blocked")
	}
	return issues
}

func predictionDue(now time.Time) bool {
	local := now.In(market.Shanghai)
	minute := local.Hour()*60 + local.Minute()
	return minute >= 9*60+35 && minute < 11*60+30 || minute >= 13*60+5 && minute < 15*60
}
