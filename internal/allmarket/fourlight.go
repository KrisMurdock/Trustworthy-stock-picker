package allmarket

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/logging"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
)

const (
	fourLightStateAllRed      = "all_red"
	fourLightStateNonAllRed   = "non_all_red"
	fourLightStateUnknown     = "unknown"
	fourLightFirstAllRed      = "first_all_red"
	fourLightContinuing       = "continuing_all_red"
	fourLightExitAllRed       = "exit_all_red"
	fourLightNonAllRed        = "non_all_red"
	fourLightLifecycleUnknown = "unknown"
	fourLightLoadBars         = 120
)

type FourLightStore interface {
	ActiveStocks(context.Context) ([]config.Stock, error)
	LoadBarsThrough(context.Context, string, string, string, int) (storage.AllMarketBarWindow, error)
	StartFourLightRun(context.Context, storage.FourLightRun) error
	CompleteFourLightRun(context.Context, storage.FourLightRun, []storage.FourLightRecord) error
	FailFourLightRun(context.Context, storage.FourLightRun) error
}

type FourLightOptions struct {
	AsOf string
}

type FourLightResult struct {
	Version          int       `json:"schema_version"`
	ID               string    `json:"run_id"`
	AsOf             string    `json:"as_of"`
	Rule             string    `json:"rule"`
	StartedAt        time.Time `json:"started_at"`
	FinishedAt       time.Time `json:"finished_at"`
	Status           string    `json:"status"`
	Total            int       `json:"total"`
	Ready            int       `json:"ready"`
	AllRed           int       `json:"all_red"`
	FirstAllRed      int       `json:"first_all_red"`
	ContinuingAllRed int       `json:"continuing_all_red"`
	ExitAllRed       int       `json:"exit_all_red"`
	NonAllRed        int       `json:"non_all_red"`
	Unknown          int       `json:"unknown"`
	ErrorKind        string    `json:"error_kind,omitempty"`
}

// FourLightRunner calculates the frozen four-light approximation for every
// active full-market stock. It never reads the production candidate report or
// changes selection, notification or delivery state.
type FourLightRunner struct {
	DB       FourLightStore
	Calendar *calendar.Calendar
	Log      *logging.Logger
	Now      func() time.Time
	Rule     string
}

func NewFourLight(c config.Config, db FourLightStore, log *logging.Logger) *FourLightRunner {
	if log == nil {
		log = logging.Nop()
	}
	return &FourLightRunner{DB: db, Calendar: calendar.New(c), Log: log, Now: time.Now, Rule: strategy.FourLightRule}
}

func (r *FourLightRunner) Run(ctx context.Context, options FourLightOptions) (FourLightResult, error) {
	result := FourLightResult{Version: 1, AsOf: options.AsOf, Rule: strategy.FourLightRule, StartedAt: r.now()}
	if r == nil || r.DB == nil || r.Calendar == nil {
		return result, errors.New("four-light runner is not configured")
	}
	if err := r.validateAsOf(&result); err != nil {
		return result, err
	}
	previousTradeDate, err := previousTradingDate(r.Calendar, result.AsOf)
	if err != nil {
		return result, err
	}
	stocks, err := r.DB.ActiveStocks(ctx)
	if err != nil {
		return result, err
	}
	if len(stocks) == 0 {
		return result, errors.New("all-market universe has no active stocks")
	}
	run := storage.FourLightRun{ID: fmt.Sprintf("%s-%d-%d", result.AsOf, r.now().UnixNano(), runSequence.Add(1)),
		AsOf: result.AsOf, Rule: result.Rule, StartedAt: result.StartedAt, FinishedAt: result.StartedAt,
		Status: "running", Total: len(stocks)}
	result.ID = run.ID
	if err := r.DB.StartFourLightRun(ctx, run); err != nil {
		return result, err
	}
	r.Log.Info("Four-light calculation started", "as_of", result.AsOf, "rule", result.Rule, "stocks", len(stocks), "previous_trade_date", previousTradeDate)

	records := make([]storage.FourLightRecord, 0, len(stocks))
	for index, stock := range stocks {
		if err := ctx.Err(); err != nil {
			return r.fail(result, run, "canceled", err)
		}
		window, loadErr := r.DB.LoadBarsThrough(ctx, stock.Code, "qfq", result.AsOf, fourLightLoadBars)
		if loadErr != nil && !errors.Is(loadErr, fs.ErrNotExist) {
			return r.fail(result, run, "storage", loadErr)
		}
		record := analyzeFourLightStock(stock, window, loadErr, result.AsOf, previousTradeDate, result.Rule, run.ID, r.now())
		records = append(records, record)
		if (index+1)%500 == 0 {
			r.Log.Info("Four-light calculation progress", "completed", index+1, "total", len(stocks), "as_of", result.AsOf)
		}
	}
	result.FinishedAt = r.now()
	for _, record := range records {
		result.Total++
		switch record.DataStatus {
		case strategy.FourLightDataReady:
			result.Ready++
		}
		switch record.State {
		case fourLightStateAllRed:
			result.AllRed++
		case fourLightStateNonAllRed:
			result.NonAllRed++
		case fourLightStateUnknown:
			result.Unknown++
		}
		switch record.Lifecycle {
		case fourLightFirstAllRed:
			result.FirstAllRed++
		case fourLightContinuing:
			result.ContinuingAllRed++
		case fourLightExitAllRed:
			result.ExitAllRed++
		}
	}
	result.Total = len(records)
	result.Status = "complete"
	run.FinishedAt = result.FinishedAt
	run.Status = "complete"
	run.Total, run.Ready, run.AllRed = result.Total, result.Ready, result.AllRed
	run.FirstAllRed, run.ContinuingAllRed, run.ExitAllRed = result.FirstAllRed, result.ContinuingAllRed, result.ExitAllRed
	run.NonAllRed, run.Unknown = result.NonAllRed, result.Unknown
	if err := r.DB.CompleteFourLightRun(ctx, run, records); err != nil {
		return r.fail(result, run, "storage", err)
	}
	r.Log.Info("Four-light calculation finished", "as_of", result.AsOf, "status", result.Status, "total", result.Total,
		"ready", result.Ready, "all_red", result.AllRed, "first", result.FirstAllRed, "continuing", result.ContinuingAllRed,
		"exit", result.ExitAllRed, "unknown", result.Unknown)
	return result, nil
}

func (r *FourLightRunner) validateAsOf(result *FourLightResult) error {
	now := r.now()
	if result.AsOf == "" {
		day, err := r.Calendar.Completed(now)
		if err != nil {
			return err
		}
		result.AsOf = day
	}
	if _, err := time.Parse("2006-01-02", result.AsOf); err != nil {
		return errors.New("as-of must be a valid date")
	}
	trading, err := r.Calendar.Trading(result.AsOf)
	if err != nil || !trading {
		return errors.New("as-of must be a covered trading date")
	}
	latest, err := r.Calendar.Completed(now)
	if err != nil || result.AsOf > latest {
		return errors.New("as-of session has not completed")
	}
	return nil
}

func (r *FourLightRunner) now() time.Time {
	if r.Now == nil {
		return time.Now()
	}
	return r.Now()
}

func (r *FourLightRunner) fail(result FourLightResult, run storage.FourLightRun, kind string, err error) (FourLightResult, error) {
	result.Status = "failed"
	result.ErrorKind = kind
	result.FinishedAt = r.now()
	run.Status = "failed"
	run.FinishedAt = result.FinishedAt
	run.ErrorKind = kind
	cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if failErr := r.DB.FailFourLightRun(cleanup, run); failErr != nil {
		err = errors.Join(err, failErr)
	}
	r.Log.Warn("Four-light calculation failed", "as_of", result.AsOf, "kind", kind)
	return result, err
}

func previousTradingDate(cal *calendar.Calendar, day string) (string, error) {
	t, err := time.ParseInLocation("2006-01-02", day, market.Shanghai)
	if err != nil {
		return "", errors.New("invalid four-light signal date")
	}
	for i := 0; i < 31; i++ {
		t = t.AddDate(0, 0, -1)
		date := calendar.Date(t)
		trading, err := cal.Trading(date)
		if err != nil {
			return "", err
		}
		if trading {
			return date, nil
		}
	}
	return "", errors.New("no previous trading day in calendar")
}

func analyzeFourLightStock(stock config.Stock, window storage.AllMarketBarWindow, loadErr error, asOf, previousTradeDate, rule, runID string, computedAt time.Time) storage.FourLightRecord {
	record := storage.FourLightRecord{
		AsOf: asOf, Rule: rule, Code: stock.Code, Name: stock.Name, PreviousTradeDate: previousTradeDate,
		DataStatus: strategy.FourLightDataUnknown, State: fourLightStateUnknown, Lifecycle: fourLightLifecycleUnknown,
		Trend: strategy.FourLightUnknown, Energy: strategy.FourLightUnknown, MidTerm: strategy.FourLightUnknown,
		ShortTerm: strategy.FourLightUnknown, UnknownLights: 4, Source: window.Source, DataField: window.DataField,
		ComputedAt: computedAt.UTC(), RunID: runID,
	}
	reasons := []string{"价量四灯近似", "未对齐目标软件", "仅使用信号日已完成日线"}
	if loadErr != nil {
		if errors.Is(loadErr, fs.ErrNotExist) {
			record.Reason = fourLightReason(reasons, "没有已完成日线")
		} else {
			record.Reason = fourLightReason(reasons, "读取全市场日线失败")
		}
		return record
	}
	record.BarCount = len(window.Bars)
	if len(window.Bars) == 0 {
		record.Reason = fourLightReason(reasons, "没有已完成日线")
		return record
	}
	record.LatestBarDate = window.Bars[len(window.Bars)-1].Date
	if record.LatestBarDate != asOf {
		record.Reason = fourLightReason(reasons, "最新日线 "+record.LatestBarDate+" 未覆盖信号日 "+asOf)
		return record
	}
	current := strategy.AnalyzeFourLight(window.Bars)
	record.Trend, record.Energy, record.MidTerm, record.ShortTerm = current.Trend, current.Energy, current.MidTerm, current.ShortTerm
	record.UnknownLights = current.UnknownLights
	if current.DataStatus == strategy.FourLightDataUnknown {
		record.Reason = fourLightReason(reasons, current.Reason)
		return record
	}
	if current.AllRed {
		record.State = fourLightStateAllRed
	} else {
		record.State = fourLightStateNonAllRed
	}

	previousIndex := -1
	for index := len(window.Bars) - 1; index >= 0; index-- {
		if window.Bars[index].Date == previousTradeDate {
			previousIndex = index
			break
		}
		if window.Bars[index].Date < previousTradeDate {
			break
		}
	}
	if previousIndex < 0 {
		record.DataStatus = strategy.FourLightDataPartial
		record.Reason = fourLightReason(reasons, "上一交易日 "+previousTradeDate+" 无已完成日线")
		return record
	}
	previous := strategy.AnalyzeFourLight(window.Bars[:previousIndex+1])
	if previous.DataStatus == strategy.FourLightDataUnknown {
		record.DataStatus = strategy.FourLightDataPartial
		record.Reason = fourLightReason(reasons, "上一交易日 "+previousTradeDate+" 日线不足或无效")
		return record
	}
	record.DataStatus = strategy.FourLightDataReady
	switch {
	case record.State == fourLightStateAllRed && !previous.AllRed:
		record.Lifecycle = fourLightFirstAllRed
	case record.State == fourLightStateAllRed && previous.AllRed:
		record.Lifecycle = fourLightContinuing
	case record.State == fourLightStateNonAllRed && previous.AllRed:
		record.Lifecycle = fourLightExitAllRed
	default:
		record.Lifecycle = fourLightNonAllRed
	}
	record.Reason = fourLightReason(reasons, "")
	return record
}

func fourLightReason(base []string, extra string) string {
	parts := append([]string{}, base...)
	if strings.TrimSpace(extra) != "" {
		parts = append(parts, extra)
	}
	return strings.Join(parts, "；")
}
