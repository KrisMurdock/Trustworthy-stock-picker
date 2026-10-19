// Package diagnostics explains saved selection evidence without rerunning a
// backtest, changing recommendations, or rewriting persisted reports.
package diagnostics

import (
	"fmt"
	"strings"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/selection"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
)

type Issue struct {
	strategy.GateFailure
	Message string `json:"message"`
}

type Rule struct {
	Name     string  `json:"name"`
	Label    string  `json:"label"`
	Status   string  `json:"status"`
	Summary  string  `json:"summary"`
	Failures []Issue `json:"failures"`
}

type Candidate struct {
	Status  string   `json:"status"`
	Summary string   `json:"summary"`
	Risks   []string `json:"risks"`
	Rules   []Rule   `json:"rules"`
}

type Validation struct {
	Status   string  `json:"status"`
	Summary  string  `json:"summary"`
	Failures []Issue `json:"failures"`
}

type Report struct {
	Version    int                  `json:"schema_version"`
	Validation Validation           `json:"validation"`
	Candidates map[string]Candidate `json:"candidates"`
}

var labels = map[string]string{
	"baseline": "股池基准", "breakout": "突破形态", "pullback": "趋势回踩", "momentum": "趋势动量", "volume_recovery": "量能恢复",
	"historical_net_loss_limit_breached": "历史单笔净亏损超限", "insufficient_or_unprofitable_evidence": "匹配规则未通过历史检验", "holdout_validation_not_passed": "独立验证未通过",
	"extended_price": "涨幅/偏离过大", "large_daily_range": "日内波幅过大", "upper_wick": "上影线较长", "cash_insufficient": "预算不足最小买入量",
	"chan_filtered": "缠论未通过", "chan_unknown": "缠论结构未知",
	"structure_guard_fail": "结构门控不通过", "structure_guard_unknown": "结构门控数据不足",
}

func Label(code string) string {
	if label, ok := labels[code]; ok {
		return label
	}
	return strings.ReplaceAll(code, "_", " ")
}

func issue(f strategy.GateFailure, s strategy.Stats) Issue {
	text := ""
	switch f.Code {
	case "loss_limit":
		count := fmt.Sprintf("%d笔", s.LossBreaches)
		if s.LossLimitPct != strategy.MaxTradeLossPct {
			count = "至少1笔（旧报告未保存精确次数）"
		}
		text = fmt.Sprintf("历史单笔净亏损超限：%s，最差净收益%+.2f%%；要求每笔净收益严格高于 -%.2f%%", count, s.WorstNet, strategy.MaxTradeLossPct)
	case "samples":
		text = fmt.Sprintf("入场样本 %.0f / 要求 ≥%.0f", f.Actual, f.Required)
	case "signal_dates":
		text = fmt.Sprintf("不同信号日 %.0f / 要求 ≥%.0f", f.Actual, f.Required)
	case "unresolved":
		text = fmt.Sprintf("未决交易 %.0f笔 / 要求 0笔（缺行情、复权事件等需核查）", f.Actual)
	case "resolved_samples":
		text = "尚无已退出样本，净收益及亏损约束未验证"
	case "mean_net":
		text = fmt.Sprintf("已退出平均净收益%+.2f%% / 要求为正", f.Actual)
	case "target_rate":
		text = fmt.Sprintf("净目标达标率%.2f%% / 要求高于股池基准%.2f%%", f.Actual, f.Required)
	case "baseline_mean_net":
		text = fmt.Sprintf("已退出平均净收益%+.2f%% / 要求高于股池基准%+.2f%%", f.Actual, f.Required)
	case "holdout_unavailable":
		text = "尚未形成独立验证区间；需足够且已完成持有期的历史信号"
	}
	return Issue{f, text}
}

func validation(r *strategy.Research) Validation {
	v := Validation{Status: "failed", Summary: "独立验证未通过", Failures: []Issue{}}
	if r.ValidationPassed {
		v.Status = "passed"
		v.Summary = "独立验证通过"
		return v
	}
	hold := r.Holdout
	switch {
	case hold.Method != "frozen_holdout":
		v.Status = "unavailable"
		v.Summary = "独立验证未建立"
	case hold.Recommended.Samples == 0:
		v.Status = "no_recommendations"
		v.Summary = "独立验证暂无结论：无合格推荐样本"
	}
	for _, f := range strategy.HoldoutFailures(hold) {
		// A zero denominator is unavailable evidence, not a measured 0% return.
		if hold.Recommended.Samples == 0 && (f.Code == "mean_net" || f.Code == "target_rate" || f.Code == "baseline_mean_net") {
			continue
		}
		if hold.Recommended.Resolved == 0 && (f.Code == "mean_net" || f.Code == "baseline_mean_net" || f.Code == "target_rate") {
			continue
		}
		v.Failures = append(v.Failures, issue(f, hold.Recommended))
	}
	if len(v.Failures) == 0 {
		v.Summary = "历史报告记录为未通过，请重新生成研究报告核对"
	}
	return v
}

func candidate(c strategy.Candidate, r *strategy.Research) Candidate {
	d := Candidate{Status: "unavailable", Summary: "历史报告未保存完整规则统计", Risks: []string{}, Rules: []Rule{}}
	rules := []string{}
	for _, rule := range c.Rules {
		if rule != "baseline" {
			rules = append(rules, rule)
		}
	}
	if len(c.Rules) > 0 && len(rules) == 0 {
		d.Status = "no_signal"
		d.Summary = "未触发买入规则；股池基准仅用于比较"
	} else if c.Evidence != nil {
		d.Status = "passed"
		d.Summary = "已存在通过历史检验的匹配规则"
	}
	if r.Parameters.Validate() == nil && len(rules) > 0 {
		var baseline strategy.Stats
		baselineFound := false
		for _, s := range r.Rules {
			if s.Rule == "baseline" && s.Target == r.Parameters.TargetNetPct && s.Horizon == r.Parameters.MaxHoldingDays {
				baseline = s
				baselineFound = true
				break
			}
		}
		passed, complete := false, true
		for _, name := range rules {
			entry := Rule{Name: name, Label: Label(name), Status: "unavailable", Summary: "规则历史统计缺失，需重新生成研究报告", Failures: []Issue{}}
			found := false
			for _, s := range r.Rules {
				if s.Rule != name || s.Target != r.Parameters.TargetNetPct || s.Horizon != r.Parameters.MaxHoldingDays {
					continue
				}
				found = true
				if !baselineFound {
					entry.Summary = "股池基准统计缺失，无法核对规则优势"
					break
				}
				entry.Status = "passed"
				entry.Summary = fmt.Sprintf("%d个入场样本 / %d个信号日；净目标%.1f%%，持有%d日", s.Samples, s.Dates, s.Target, s.Horizon)
				for _, f := range strategy.EvidenceFailures(s, baseline, r.Parameters) {
					entry.Status = "failed"
					if s.Samples == 0 && (f.Code == "mean_net" || f.Code == "target_rate") {
						continue
					}
					if s.Resolved == 0 && (f.Code == "mean_net" || f.Code == "target_rate") {
						continue
					}
					entry.Failures = append(entry.Failures, issue(f, s))
				}
				if entry.Status == "passed" {
					passed = true
				}
				break
			}
			if !found || !baselineFound {
				complete = false
			}
			d.Rules = append(d.Rules, entry)
		}
		if passed {
			d.Status = "passed"
			d.Summary = "已存在通过历史检验的匹配规则"
		} else if complete {
			d.Status = "failed"
			d.Summary = "匹配规则未通过历史检验（全股池规则样本）"
		}
	}
	for _, risk := range c.Risks {
		switch risk {
		case "insufficient_or_unprofitable_evidence":
			d.Risks = append(d.Risks, d.Summary)
		case "holdout_validation_not_passed": // Shown once for the whole report.
		default:
			d.Risks = append(d.Risks, Label(risk))
		}
	}
	if d.Status == "no_signal" && !contains(d.Risks, d.Summary) {
		d.Risks = append(d.Risks, d.Summary)
	}
	return d
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

// Explain is a versioned, additive read model. Stored rankings, IDs and research
// parameters remain unchanged, including for reports generated by older builds.
func Explain(r selection.Report) *Report {
	if r.Research == nil {
		return nil
	}
	d := &Report{Version: 1, Validation: validation(r.Research), Candidates: map[string]Candidate{}}
	for _, c := range r.Ranking {
		d.Candidates[c.Code] = candidate(c, r.Research)
	}
	for _, c := range r.Selected {
		if c.Ranking != nil {
			d.Candidates[c.Code] = candidate(*c.Ranking, r.Research)
		}
	}
	return d
}
