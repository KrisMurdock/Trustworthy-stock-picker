package dayrange

import (
	"context"
	"sort"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	notifyreport "github.com/KrisMurdock/Trustworthy-stock-picker/internal/notify/report"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/secondstage"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/selection"
)

func (s *Service) publishPremarketArticle(ctx context.Context, target string, predictions []Prediction, skipped []string, generatedAt, now time.Time) error {
	if s == nil || !s.Config.DayRange.PushEnabled || len(predictions) == 0 {
		return nil
	}
	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()
	channelNames := s.articleChannelsLocked()
	if s.deliveryStore == nil || len(channelNames) == 0 {
		return nil
	}
	report, err := s.loadExecutionReport(ctx, target, now)
	if err != nil {
		return err
	}
	names := make(map[string]string, len(s.Config.Stocks))
	for _, stock := range s.Config.Stocks {
		names[stock.Code] = stock.Name
	}
	forecasts := make(map[string]selection.DayRangeForecast, len(predictions))
	inputThrough := ""
	for _, prediction := range predictions {
		if prediction.TargetDate != "" && prediction.TargetDate != target {
			continue
		}
		forecast := selection.DayRangeForecast{
			Code: prediction.Code, TargetDate: target, ModelID: prediction.ModelID, Source: prediction.Source,
			ReferencePrice: prediction.ReferencePrice, PointHigh: prediction.PointHigh, PointLow: prediction.PointLow,
		}
		if prediction.IntervalHigh != nil {
			forecast.HighLower = prediction.IntervalHigh.Lower
			forecast.HighUpper = prediction.IntervalHigh.Upper
			forecast.HighCoverage = prediction.IntervalHigh.NominalCoverage
		}
		if prediction.IntervalLow != nil {
			forecast.LowLower = prediction.IntervalLow.Lower
			forecast.LowUpper = prediction.IntervalLow.Upper
			forecast.LowCoverage = prediction.IntervalLow.NominalCoverage
		}
		forecasts[prediction.Code] = forecast
		if prediction.InputThrough > inputThrough {
			inputThrough = prediction.InputThrough
		}
	}
	if len(forecasts) == 0 {
		return nil
	}
	statuses := premarketArticleStatuses(report, forecasts)
	message, ok, err := notifyreport.BuildPremarketArticle(notifyreport.PremarketArticleInput{
		TargetDate: target, GeneratedAt: generatedAt, InputThrough: inputThrough,
		PoolSize: len(s.Config.Stocks), Report: report, Predictions: forecasts,
		Names: names, Statuses: statuses, Skipped: skipped,
	})
	if err != nil || !ok {
		return err
	}
	if err := s.ledger.EnqueuePart(target, message, channelNames, "article", 0, ""); err != nil {
		return err
	}
	if err := s.saveLedgerLocked(); err != nil {
		return err
	}
	return s.flushLocked(ctx, s.now())
}

func premarketArticleStatuses(report *selection.Report, forecasts map[string]selection.DayRangeForecast) map[string]notifyreport.PremarketExecutionStatus {
	statuses := make(map[string]notifyreport.PremarketExecutionStatus, len(forecasts))
	for code := range forecasts {
		statuses[code] = notifyreport.PremarketExecutionStatus{Status: "观察", Reason: "未进入精排候选"}
	}
	if report == nil || report.SecondStage == nil || report.SecondStage.Status != secondstage.StatusOK {
		for code := range forecasts {
			statuses[code] = notifyreport.PremarketExecutionStatus{Status: "观察", Reason: "精排队列尚未就绪"}
		}
		return statuses
	}
	plans := executionPlans(*report)
	for _, candidate := range report.SecondStage.Candidates {
		forecast, ok := forecasts[candidate.Code]
		if !ok {
			continue
		}
		prediction := predictionFromForecast(forecast)
		entry := plans[candidate.Code]
		status, reason := executionStatus(&prediction, entry.plan, entry.formal)
		statuses[candidate.Code] = notifyreport.PremarketExecutionStatus{Status: status, Reason: reason}
	}
	return statuses
}

func predictionFromForecast(forecast selection.DayRangeForecast) Prediction {
	prediction := Prediction{
		Code: forecast.Code, TargetDate: forecast.TargetDate, ModelID: forecast.ModelID, Source: forecast.Source,
		ReferencePrice: forecast.ReferencePrice, PointHigh: forecast.PointHigh, PointLow: forecast.PointLow,
	}
	if forecast.HighLower > 0 && forecast.HighUpper >= forecast.HighLower {
		prediction.IntervalHigh = &Interval{Lower: forecast.HighLower, Upper: forecast.HighUpper, NominalCoverage: forecast.HighCoverage}
	}
	if forecast.LowLower > 0 && forecast.LowUpper >= forecast.LowLower {
		prediction.IntervalLow = &Interval{Lower: forecast.LowLower, Upper: forecast.LowUpper, NominalCoverage: forecast.LowCoverage}
	}
	return prediction
}

func (s *Service) articleChannelsLocked() []string {
	available := map[string]bool{}
	for _, channel := range s.noticeChannels {
		if channel != nil && channel.Name() != "" {
			available[channel.Name()] = true
		}
	}
	names := make([]string, 0)
	for _, name := range s.Config.ChannelNamesForRoute(config.NotifyRouteWechatPremarket) {
		if available[name] {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}
