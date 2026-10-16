package experiment

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"time"
)

// ResultSnapshot preserves measured outcomes, separately from a read-time
// review under a newer acceptance policy. Null means unavailable, never zero.
type ResultSnapshot struct {
	Version      int               `json:"schema_version"`
	ID           string            `json:"id"`
	Source       string            `json:"source"`
	ExperimentID string            `json:"experiment_id"`
	DataID       string            `json:"data_snapshot_id"`
	Engine       string            `json:"engine"`
	CodeVersion  string            `json:"code_version"`
	FinishedAt   *time.Time        `json:"finished_at"`
	Rows         []ResultRow       `json:"rows"`
	Notes        []string          `json:"notes"`
	InputSHA     map[string]string `json:"input_sha256,omitempty"`
}

type ResultRow struct {
	ID            string   `json:"id"`
	Label         string   `json:"label"`
	Period        string   `json:"period"`
	From          string   `json:"from"`
	Through       string   `json:"through"`
	Rule          string   `json:"rule"`
	Target        float64  `json:"target_net_pct"`
	Stop          *float64 `json:"stop_loss_pct"` // Zero explicitly means no stop.
	Horizon       int      `json:"sellable_days"`
	Batches       int      `json:"max_batches"`
	Samples       int      `json:"samples"`
	Resolved      int      `json:"resolved"`
	Unresolved    int      `json:"unresolved"`
	Skipped       int      `json:"skipped_entries"`
	Targets       *int     `json:"targets"`
	Rate          *float64 `json:"target_rate"`
	TargetDays    *float64 `json:"mean_target_days"`
	HoldingDays   *float64 `json:"mean_resolved_holding_days"`
	MeanNet       *float64 `json:"mean_resolved_net_pct"`
	MaxLoss       *float64 `json:"max_realized_loss_pct"`
	MaxAdverse    *float64 `json:"max_adverse_loss_pct"`
	PenalizedDays *float64 `json:"mean_target_days_failure_21"`
}

func Value[T any](v T) *T { return &v }

func (s *ResultSnapshot) Seal() error {
	s.ID = ""
	b, err := json.Marshal(s)
	if err != nil {
		return errors.New("invalid result snapshot")
	}
	digest := sha256.Sum256(b)
	s.ID = hex.EncodeToString(digest[:])
	return nil
}

func (s ResultSnapshot) Validate(r Record) error {
	if s.Version != 1 || s.ExperimentID != r.ID || s.DataID != r.Data.ID || s.Engine != r.Engine || s.CodeVersion != r.CodeVersion || len(s.Rows) == 0 || len(s.Rows) > 1000 {
		return errors.New("result snapshot metadata mismatch")
	}
	if (s.FinishedAt == nil) != (r.FinishedAt == nil) || s.FinishedAt != nil && !s.FinishedAt.Equal(*r.FinishedAt) {
		return errors.New("result snapshot completion time mismatch")
	}
	id := s.ID
	if err := s.Seal(); err != nil || id != s.ID {
		return errors.New("result snapshot digest mismatch")
	}
	seen := map[string]bool{}
	for _, x := range s.Rows {
		if x.ID == "" || seen[x.ID] || x.Label == "" || x.Target <= 0 || x.Horizon < 1 || x.Batches < 1 || x.Samples < 0 || x.Resolved < 0 || x.Unresolved < 0 || x.Skipped < 0 || x.Resolved+x.Unresolved != x.Samples {
			return errors.New("invalid result snapshot counts or policy")
		}
		seen[x.ID] = true
		if (x.Rate == nil) != (x.Targets == nil) || x.Targets != nil && (*x.Targets < 0 || *x.Targets > x.Samples || x.Samples == 0 || math.Abs(*x.Rate-float64(*x.Targets)/float64(x.Samples)) > 1e-9) {
			return errors.New("invalid result snapshot target rate")
		}
		if x.TargetDays != nil && (x.Targets == nil || *x.Targets == 0 || *x.TargetDays < 1 || *x.TargetDays > float64(x.Horizon)) {
			return errors.New("invalid result snapshot target days")
		}
		if x.Resolved == 0 && (x.MeanNet != nil || x.HoldingDays != nil || x.MaxLoss != nil || x.MaxAdverse != nil) {
			return errors.New("empty result snapshot has realized metrics")
		}
		if x.MaxLoss != nil && *x.MaxLoss < 0 || x.MaxAdverse != nil && *x.MaxAdverse < 0 || x.Stop != nil && *x.Stop < 0 {
			return errors.New("invalid result snapshot loss magnitude")
		}
	}
	return nil
}

// SnapshotResult reads the statistics originally stored by the run. It never
// uses Review, current market data, or today's strategy.Summarize implementation.
func SnapshotResult(r Record) *ResultSnapshot {
	if r.Status != "completed" || r.Result == nil {
		return nil
	}
	d := r.Result.Development.Research
	x := d.Development.TopRanked
	p := d.Parameters
	if p.MaxHoldingDays < 1 {
		p = r.Data.Parameters
	}
	if r.Spec.NextDayRule != "" {
		p.MaxHoldingDays = 1
	}
	s := &ResultSnapshot{Version: 1, Source: "archived_statistics", ExperimentID: r.ID, DataID: r.Data.ID, Engine: r.Engine, CodeVersion: r.CodeVersion, FinishedAt: r.FinishedAt, Rows: []ResultRow{}, Notes: []string{
		"指标来自实验原始存档，不随当前规则复核或行情同步改变。",
		"达标平均持仓日只统计退出前已确认最高价净触达的交易；买入后的第一个可卖出交易日记为1，不要求收盘价达到目标。触达不等于实际止盈成交。",
		"最大已实现单笔亏损按实际投入扣除成本计算，仅统计已结束交易；不是账户最大回撤。零表示已结束样本无亏损，未记录或无样本显示为空。",
		"未决样本保留在触达率分母；全部已结束交易的平均持仓和仅达标交易的平均持仓分别展示。",
	}}
	for i, target := range []float64{2.5, 3} {
		row := ResultRow{ID: []string{"target25", "target30"}[i], Label: r.Spec.Title, Period: "开发验证", From: d.Development.From, Through: d.Development.Through, Rule: r.Spec.Change, Target: target, Stop: Value(math.Abs(p.StopNetPct)), Horizon: p.MaxHoldingDays, Batches: 1, Samples: x.Samples, Resolved: x.Resolved, Unresolved: x.Samples - x.Resolved, Skipped: x.SkippedEntries}
		if x.HighTouchSamples > 0 && x.Samples > 0 {
			n, days := x.HighTouch25, x.HighTouch25Days
			if i == 1 {
				n, days = x.HighTouch30, x.HighTouch30Days
			}
			row.Targets, row.Rate = Value(n), Value(float64(n)/float64(x.Samples))
			if n > 0 && days >= 1 && days <= float64(p.MaxHoldingDays) {
				row.TargetDays = Value(days)
			}
		}
		if x.Resolved > 0 {
			row.HoldingDays, row.MeanNet, row.MaxLoss = Value(x.MeanDays), Value(x.MeanNet), Value(math.Max(0, -x.WorstNet))
			if r.Engine == Engine {
				row.MaxAdverse = Value(math.Max(0, -x.WorstMAE))
			}
		}
		s.Rows = append(s.Rows, row)
	}
	if s.Seal() != nil {
		return nil
	}
	return s
}
