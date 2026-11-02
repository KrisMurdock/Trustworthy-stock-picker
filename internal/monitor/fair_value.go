package monitor

import (
	"context"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/fairvalue"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/secondstage"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/selection"
)

const fairValueStockLimit = 10

type fairValueAssessor interface {
	Assess(context.Context, config.Stock, float64, string) (fairvalue.Assessment, error)
}

// enrichFairValues attaches valuation bands to the report that will be saved
// and reused by both the daily and premarket articles. A source failure is
// recorded as an explicit unavailable estimate and never blocks publication.
func (m *Monitor) enrichFairValues(ctx context.Context, report *selection.Report) {
	if m == nil || report == nil || m.FairValues == nil || ctx.Err() != nil {
		return
	}
	prices := make(map[string]float64, len(report.Ranking))
	for _, candidate := range report.Ranking {
		if candidate.Code != "" && candidate.Close > 0 {
			prices[candidate.Code] = candidate.Close
		}
	}
	adjust := report.Adjust
	if adjust == "" {
		adjust = m.Config.Daily.Adjust
	}
	requiresUnadjusted := adjust != "" && adjust != "none"
	if !requiresUnadjusted {
		for _, candidate := range report.Selected {
			if candidate.Code != "" && candidate.Close > 0 {
				if _, ok := prices[candidate.Code]; !ok {
					prices[candidate.Code] = candidate.Close
				}
			}
		}
	}
	codes := fairValueCodes(*report)
	if len(codes) == 0 {
		return
	}
	assessor, hasAssessor := m.FairValues.(fairValueAssessor)
	report.FairValues = make(map[string]fairvalue.Estimate, len(codes))
	if hasAssessor {
		report.FairValuesV2 = make(map[string]fairvalue.EstimateV2, len(codes))
	} else {
		report.FairValuesV2 = nil
	}
	markUnavailable := func(code, reason string) {
		report.FairValues[code] = unavailableFairValue(code, report.AsOf, reason)
		if hasAssessor {
			report.FairValuesV2[code] = unavailableFairValueV2(code, report.AsOf, reason)
		}
	}
	if !m.Config.Strategy.Enabled && requiresUnadjusted {
		for _, code := range codes {
			markUnavailable(code, "需要不复权收盘价")
		}
		return
	}
	stocks := make(map[string]config.Stock, len(m.Config.Stocks))
	for _, stock := range m.Config.Stocks {
		stocks[stock.Code] = stock
	}
	fetchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for index, code := range codes {
		stock, ok := stocks[code]
		if !ok {
			continue
		}
		price := prices[code]
		if price <= 0 {
			reason := "收盘价缺失"
			if requiresUnadjusted {
				reason = "需要不复权收盘价"
			}
			markUnavailable(code, reason)
			continue
		}
		if hasAssessor {
			assessment, err := assessor.Assess(fetchCtx, stock, price, report.AsOf)
			if err != nil {
				m.Log.Warn("Fair value snapshot failed", "code", code, "as_of", report.AsOf, "error", err.Error())
				markUnavailable(code, "财务估值数据获取失败")
			} else {
				report.FairValues[code] = assessment.V1
				report.FairValuesV2[code] = assessment.V2
			}
		} else {
			estimate, err := m.FairValues.Estimate(fetchCtx, stock, price, report.AsOf)
			if err != nil {
				m.Log.Warn("Fair value snapshot failed", "code", code, "as_of", report.AsOf, "error", err.Error())
				report.FairValues[code] = unavailableFairValue(code, report.AsOf, "财务估值数据获取失败")
			} else {
				report.FairValues[code] = estimate
			}
		}
		if fetchCtx.Err() != nil {
			for _, remaining := range codes[index+1:] {
				markUnavailable(remaining, "估值请求超时")
			}
			break
		}
	}
}

func fairValueCodes(report selection.Report) []string {
	seen := map[string]bool{}
	codes := make([]string, 0, fairValueStockLimit)
	add := func(code string) {
		if code == "" || seen[code] || len(codes) >= fairValueStockLimit {
			return
		}
		seen[code] = true
		codes = append(codes, code)
	}
	if report.SecondStage != nil && report.SecondStage.Status == secondstage.StatusOK {
		for _, candidate := range report.SecondStage.Candidates {
			add(candidate.Code)
		}
	}
	for _, candidate := range report.Selected {
		add(candidate.Code)
	}
	for _, candidate := range report.Ranking {
		add(candidate.Code)
	}
	return codes
}

func unavailableFairValue(code, asOf, reason string) fairvalue.Estimate {
	return fairvalue.Estimate{Version: fairvalue.Version, Code: code, AsOf: asOf, Source: "sina",
		Method: fairvalue.Method, Reason: reason}
}

func unavailableFairValueV2(code, asOf, reason string) fairvalue.EstimateV2 {
	return fairvalue.EstimateV2{Version: fairvalue.VersionV2, Code: code, AsOf: asOf, Source: "sina",
		Method: fairvalue.MethodV2, DataQuality: fairvalue.DataQualityUnavailable, Reason: reason}
}
