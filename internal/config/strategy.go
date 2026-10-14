package config

import (
	"errors"
	"math"
)

// Strategy is an independently versioned, optional extension to configuration v1.
type Strategy struct {
	Version         int     `yaml:"schema_version" json:"schema_version"`
	Enabled         bool    `yaml:"enabled" json:"enabled"`
	TargetNetPct    float64 `yaml:"target_net_pct" json:"target_net_pct"`
	StopNetPct      float64 `yaml:"stop_net_pct" json:"stop_net_pct"`
	MaxHoldingDays  int     `yaml:"max_holding_days" json:"max_holding_days"`
	TradeCash       float64 `yaml:"trade_cash" json:"trade_cash"`
	CommissionRate  float64 `yaml:"commission_rate" json:"commission_rate"`
	MinCommission   float64 `yaml:"min_commission" json:"min_commission"`
	TransferRate    float64 `yaml:"transfer_rate" json:"transfer_rate"`
	SellStampRate   float64 `yaml:"sell_stamp_rate" json:"sell_stamp_rate"`
	SlippageRate    float64 `yaml:"slippage_rate" json:"slippage_rate"`
	MaxEntryGapPct  float64 `yaml:"max_entry_gap_pct" json:"max_entry_gap_pct"`
	MinEvidence     int     `yaml:"min_evidence" json:"min_evidence"`
	MinTrainingDays int     `yaml:"min_training_days" json:"min_training_days"`
	HoldoutDays     int     `yaml:"holdout_days" json:"holdout_days"`
	MinRankScore    float64 `yaml:"min_rank_score" json:"min_rank_score"`
}

func DefaultStrategy() Strategy {
	return Strategy{Version: 1, TargetNetPct: 3, StopNetPct: -3, MaxHoldingDays: 5,
		TradeCash: 20000, CommissionRate: 0.0001, MinCommission: 5,
		TransferRate: 0.00001, SellStampRate: 0.0005, SlippageRate: 0.0005,
		MaxEntryGapPct: 3, MinEvidence: 60, MinTrainingDays: 40, HoldoutDays: 40, MinRankScore: 60}
}

func (s Strategy) Validate() error {
	if (s.Version != 0 && s.Version != 1) || (s.Enabled && s.Version != 1) {
		return errors.New("strategy.schema_version must be 1")
	}
	for _, v := range []float64{s.TargetNetPct, s.StopNetPct, s.TradeCash, s.CommissionRate, s.MinCommission, s.TransferRate, s.SellStampRate, s.SlippageRate, s.MaxEntryGapPct, s.MinRankScore} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return errors.New("strategy values must be finite")
		}
	}
	if (s.TargetNetPct != 2.5 && s.TargetNetPct != 3) || s.StopNetPct >= 0 || s.StopNetPct < -20 || s.MaxHoldingDays < 1 || s.MaxHoldingDays > 10 || s.TradeCash < 1000 || s.TradeCash > 1e8 || s.MinCommission < 0 || s.MinCommission > 100 || s.MaxEntryGapPct < 0 || s.MaxEntryGapPct > 20 || s.MinRankScore < 0 || s.MinRankScore > 100 {
		return errors.New("invalid strategy trading parameters")
	}
	for _, rate := range []float64{s.CommissionRate, s.TransferRate, s.SellStampRate, s.SlippageRate} {
		if rate < 0 || rate > 0.01 {
			return errors.New("strategy fee rates must be fractions between 0 and 0.01")
		}
	}
	if s.MinEvidence < 10 || s.MinEvidence > 100000 || s.MinTrainingDays < 20 || s.MinTrainingDays > 250 || s.HoldoutDays < 10 || s.HoldoutDays > 120 {
		return errors.New("invalid strategy evidence requirements")
	}
	return nil
}
