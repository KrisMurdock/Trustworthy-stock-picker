package selection

import (
	"os"
	"path/filepath"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/secondstage"
)

func secondStageBlendPlan(c config.Config) secondstage.BlendPlan {
	plan := secondstage.BlendPlan{
		ID:               c.SecondStage.BlendID,
		FirstStageWeight: c.SecondStage.FirstStageWeight,
		Models:           make([]secondstage.BlendWeight, 0, len(c.SecondStage.Models)),
		GroupSize:        c.SecondStage.GroupSize,
		Calibration:      c.SecondStage.Calibration,
		CalibrationParam: c.SecondStage.CalibrationParam,
		RiskEta:          c.SecondStage.RiskEta,
	}
	for _, model := range c.SecondStage.Models {
		plan.Models = append(plan.Models, secondstage.BlendWeight{Name: model.Name, Weight: model.Weight})
	}
	return plan
}

func loadSecondStageModels(c config.Config, asOf string) []secondstage.ModelInput {
	sources := make([]secondstage.ModelSource, 0, len(c.SecondStage.Models))
	for _, model := range c.SecondStage.Models {
		path := filepath.Join(c.DataDir, "state", "secondstage", model.Name, asOf+".json")
		if model.Name == secondstage.ModelJEV {
			legacy := filepath.Join(c.DataDir, "state", "jev", asOf+".json")
			if _, err := os.Stat(path); os.IsNotExist(err) {
				path = legacy
			}
		}
		sources = append(sources, secondstage.ModelSource{Name: model.Name, Path: path})
	}
	return secondstage.LoadModels(sources, asOf)
}
