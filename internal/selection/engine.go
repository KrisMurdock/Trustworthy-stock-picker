package selection

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/firststage"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/secondstage"
	"time"
)

// Engine is a cooperative, in-process calculation boundary. Implementations
// must honor cancellation and return only stocks from the supplied frozen pool.
// Collection, coverage, persistence and notification are owned by the framework.
type Engine interface {
	Descriptor() EngineDescriptor
	Analyze(context.Context, Input) (Report, error)
}
type EngineDescriptor struct {
	ID            string   `json:"id"`
	Version       int      `json:"version"`
	Periods       []string `json:"periods"`
	Adjust        string   `json:"adjust"`
	RawRequired   bool     `json:"raw_required"`
	FinalOnly     bool     `json:"final_only"`
	MissingPolicy string   `json:"missing_policy"`
}
type Input struct {
	Version   int
	Config    config.Config // SafeConfig only; no paths, credentials or network clients.
	Histories []market.History
	Raw       []market.History
	Minutes   []market.MinuteHistory
	// SecondStageModels are frozen, already validated offline model snapshots.
	// Execute loads them before SafeConfig strips filesystem paths; engines
	// never read artifacts from disk themselves.
	SecondStageModels []secondstage.ModelInput
	AsOf, Target      string
	Now               time.Time
}
type Provenance struct {
	Engine     EngineDescriptor `json:"engine"`
	ConfigID   string           `json:"config_id"`
	Build      string           `json:"build"`
	Parameters json.RawMessage  `json:"parameters"`
}

func SafeConfig(c config.Config) config.Config {
	out := config.Config{Version: c.Version, Strategy: c.Strategy}
	out.StructureGuard = c.StructureGuard
	out.FirstStage = c.FirstStage
	out.SecondStage = c.SecondStage
	out.Stocks = append([]config.Stock(nil), c.Stocks...)
	out.Analysis = c.Analysis
	out.Daily = c.Daily
	out.Selection = c.Selection
	out.Calendar = c.Calendar
	out.Calendar.Holidays = append([]string(nil), c.Calendar.Holidays...)
	out.Sync = c.Sync
	return out
}
func ConfigDigest(c config.Config) (string, json.RawMessage) {
	data, _ := json.Marshal(SafeConfig(c))
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), data
}

type builtinEngine struct{ descriptor EngineDescriptor }

func (e builtinEngine) Descriptor() EngineDescriptor { return e.descriptor }
func (e builtinEngine) Analyze(ctx context.Context, in Input) (Report, error) {
	return buildWithExecution(ctx, in.Config, in.Histories, in.Raw, in.Minutes, in.SecondStageModels, in.AsOf, in.Target, in.Now)
}
func DefaultEngine(c config.Config) (Engine, error) {
	id := "leader_daily_v1"
	if c.Strategy.Enabled {
		id = "short_horizon_v1"
	}
	if c.Analysis.Engine != "auto" && c.Analysis.Engine != "" && c.Analysis.Engine != id {
		return nil, errors.New("analysis.engine does not match enabled strategy")
	}
	periods := []string{"day"}
	if c.Strategy.Enabled {
		periods = append(periods, "30", "5")
	}
	return builtinEngine{EngineDescriptor{ID: id, Version: 1, Periods: periods, Adjust: c.Daily.Adjust, RawRequired: c.Strategy.Enabled, FinalOnly: true, MissingPolicy: "exclude"}}, nil
}
func Execute(ctx context.Context, e Engine, in Input, build string) (Report, error) {
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	d := e.Descriptor()
	legacyMinuteContract := len(d.Periods) == 2 && d.Periods[0] == "day" && d.Periods[1] == "5"
	minuteContract := len(d.Periods) == 3 && d.Periods[0] == "day" && d.Periods[1] == "30" && d.Periods[2] == "5"
	dailyContract := len(d.Periods) == 1 && d.Periods[0] == "day"
	if d.ID == "" || d.Version != 1 || (!dailyContract && !legacyMinuteContract && !minuteContract) || (len(in.Minutes) > 0 && !legacyMinuteContract && !minuteContract) || d.Adjust != in.Config.Daily.Adjust || !d.FinalOnly || d.MissingPolicy != "exclude" {
		return Report{}, errors.New("unsupported algorithm input contract")
	}
	allowedPeriod := map[int]bool{}
	for _, period := range d.Periods[1:] {
		switch period {
		case "5":
			allowedPeriod[5] = true
		case "15":
			allowedPeriod[15] = true
		case "30":
			allowedPeriod[30] = true
		case "60":
			allowedPeriod[60] = true
		}
	}
	if d.RawRequired && len(in.Raw) == 0 {
		return Report{}, errors.New("algorithm requires raw execution histories")
	}
	if in.SecondStageModels == nil && in.Config.FirstStage.Enabled && in.Config.SecondStage.Enabled {
		in.SecondStageModels = loadSecondStageModels(in.Config, in.AsOf)
	}
	if err := secondstage.ValidateModelInputs(in.SecondStageModels); err != nil {
		return Report{}, err
	}
	in.Version = 1
	in.Config = SafeConfig(in.Config)
	// Own a deep copy: an algorithm must not modify the durable collection input.
	data, err := json.Marshal(in)
	if err != nil {
		return Report{}, err
	}
	var frozen Input
	if err = json.Unmarshal(data, &frozen); err != nil {
		return Report{}, err
	}
	allowed := map[string]bool{}
	for _, s := range in.Config.Stocks {
		allowed[s.Code] = true
	}
	available, execution := map[string]bool{}, map[string]bool{}
	for _, h := range frozen.Histories {
		if !allowed[h.Code] || available[h.Code] || h.Adjust != d.Adjust {
			return Report{}, errors.New("algorithm history identity mismatch")
		}
		available[h.Code] = true
	}
	for _, h := range frozen.Raw {
		if !allowed[h.Code] || execution[h.Code] || h.Adjust != "none" {
			return Report{}, errors.New("algorithm raw history identity mismatch")
		}
		execution[h.Code] = true
	}
	if d.RawRequired {
		for code := range available {
			if !execution[code] {
				return Report{}, errors.New("algorithm raw history missing for stock")
			}
		}
	}
	for _, h := range append(append([]market.History{}, frozen.Histories...), frozen.Raw...) {
		if h.AsOf != in.AsOf || h.Validate() != nil {
			return Report{}, errors.New("invalid algorithm input history")
		}
	}
	minutePeriods := map[string]map[int]bool{}
	minuteByCodePeriod := map[string]map[int]market.MinuteHistory{}
	minuteMeta := map[string]market.MinuteHistory{}
	for _, h := range frozen.Minutes {
		if !available[h.Code] || !allowedPeriod[h.Period] || h.Validate() != nil {
			return Report{}, errors.New("invalid algorithm minute history")
		}
		if minutePeriods[h.Code] == nil {
			minutePeriods[h.Code] = map[int]bool{}
			minuteByCodePeriod[h.Code] = map[int]market.MinuteHistory{}
			minuteMeta[h.Code] = h
		} else {
			first := minuteMeta[h.Code]
			if first.Source != h.Source || first.Adjust != h.Adjust || first.VolumeUnit != h.VolumeUnit || first.BasePeriod != h.BasePeriod || !first.FetchedAt.Equal(h.FetchedAt) {
				return Report{}, errors.New("algorithm minute histories are not from one snapshot")
			}
		}
		if minutePeriods[h.Code][h.Period] {
			return Report{}, errors.New("duplicate algorithm minute period")
		}
		minutePeriods[h.Code][h.Period] = true
		minuteByCodePeriod[h.Code][h.Period] = h
		for _, bar := range h.Bars {
			if !bar.Final || bar.End.In(market.Shanghai).Format("2006-01-02") > in.AsOf {
				return Report{}, errors.New("algorithm minute history is not final as of report date")
			}
		}
	}
	for _, set := range minuteByCodePeriod {
		base, hasBase := set[5]
		derived, hasDerived := set[30]
		if hasBase && hasDerived {
			if err := market.ValidateMinutePair(base, derived); err != nil {
				return Report{}, errors.New("algorithm minute histories are not from one snapshot")
			}
		}
	}
	result, err := e.Analyze(ctx, frozen)
	if err != nil {
		return Report{}, err
	}
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	if result.SelectionEnabled != in.Config.Selection.Enabled || (!in.Config.Selection.Enabled && len(result.Selected) > 0) || result.ID == "" || (result.Version != 1 && result.Version != 2 && result.Version != 3) || result.AsOf != in.AsOf || result.Target != in.Target || result.Adjust != d.Adjust || result.Strategy != d.ID {
		return Report{}, errors.New("invalid algorithm result identity")
	}
	seen := map[string]bool{}
	for _, c := range result.Selected {
		if !available[c.Code] || seen[c.Code] {
			return Report{}, errors.New("invalid algorithm selected stock")
		}
		seen[c.Code] = true
	}
	ranked := map[string]bool{}
	for _, c := range result.Ranking {
		if !available[c.Code] || ranked[c.Code] {
			return Report{}, errors.New("invalid algorithm ranking stock")
		}
		ranked[c.Code] = true
	}
	if first := result.FirstStage; first != nil {
		if first.Version != firststage.ReportSchemaVersion || first.AsOf != in.AsOf || first.Target != in.Target {
			return Report{}, errors.New("invalid first-stage report identity")
		}
		switch first.Status {
		case firststage.StatusOK:
			if first.Budget < 1 || len(first.Candidates) == 0 || len(first.Candidates) > first.Budget {
				return Report{}, errors.New("invalid first-stage candidate count")
			}
		case firststage.StatusEmpty, firststage.StatusFailed:
			if len(first.Candidates) != 0 {
				return Report{}, errors.New("invalid first-stage empty report")
			}
		default:
			return Report{}, errors.New("invalid first-stage status")
		}
		firstSeen := map[string]bool{}
		for _, candidate := range first.Candidates {
			if !available[candidate.Code] || firstSeen[candidate.Code] || candidate.Rank < 1 {
				return Report{}, errors.New("invalid first-stage candidate stock")
			}
			firstSeen[candidate.Code] = true
		}
	}
	if second := result.SecondStage; second != nil {
		if result.FirstStage == nil {
			return Report{}, errors.New("second-stage report requires first-stage candidates")
		}
		if second.Version != secondstage.ReportSchemaVersion || second.AsOf != in.AsOf || second.Target != in.Target {
			return Report{}, errors.New("invalid second-stage report identity")
		}
		if second.Ranker != "" && second.Ranker != secondstage.RankerBlendWeighted {
			return Report{}, errors.New("unsupported second-stage ranker")
		}
		if second.Ranker == secondstage.RankerBlendWeighted {
			if err := second.Blend.Validate(); err != nil {
				return Report{}, err
			}
		}
		switch second.Status {
		case secondstage.StatusOK:
			if second.TopN < 1 || second.TopN > 100 || len(second.Candidates) == 0 || len(second.Candidates) > second.TopN {
				return Report{}, errors.New("invalid second-stage candidate count")
			}
		case secondstage.StatusEmpty, secondstage.StatusFailed:
			if len(second.Candidates) != 0 {
				return Report{}, errors.New("invalid second-stage empty report")
			}
		default:
			return Report{}, errors.New("invalid second-stage status")
		}
		secondSeen := map[string]bool{}
		firstCodes := map[string]bool{}
		for _, candidate := range result.FirstStage.Candidates {
			firstCodes[candidate.Code] = true
		}
		secondRanks := map[int]bool{}
		for _, candidate := range second.Candidates {
			if candidate.Rank < 1 || candidate.Rank > len(second.Candidates) || secondRanks[candidate.Rank] || secondSeen[candidate.Code] || !firstCodes[candidate.Code] {
				return Report{}, errors.New("invalid second-stage candidate stock")
			}
			if second.Ranker == secondstage.RankerBlendWeighted &&
				(candidate.FirstStagePercentile < 0 || candidate.FirstStagePercentile > 1 || candidate.BlendScore < 0 || candidate.BlendScore > 1) {
				return Report{}, errors.New("invalid second-stage blend score")
			}
			for _, signal := range candidate.Models {
				if signal.Percentile < 0 || signal.Percentile > 1 {
					return Report{}, errors.New("invalid second-stage model percentile")
				}
			}
			secondRanks[candidate.Rank] = true
			secondSeen[candidate.Code] = true
		}
	}
	if _, err := json.Marshal(result); err != nil {
		return Report{}, errors.New("invalid algorithm numeric result")
	}
	digest, parameters := ConfigDigest(in.Config)
	result.Provenance = &Provenance{Engine: d, ConfigID: digest, Build: build, Parameters: parameters}
	return result, nil
}
