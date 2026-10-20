package predictor

import (
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/selection"
)

// ReportUniverse exposes the same selected-or-observed universe rule used by
// the production predictor and offline evaluation.
func ReportUniverse(report selection.Report, pool []config.Stock, topN int) ([]config.Stock, bool) {
	return reportUniverse(report, pool, topN)
}
