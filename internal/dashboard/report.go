package dashboard

import (
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/diagnostics"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/selection"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

type reportView struct {
	selection.Report
	Diagnostics *diagnostics.Report `json:"diagnostics,omitempty"`
}

func explainReport(r selection.Report) reportView {
	return reportView{Report: r, Diagnostics: diagnostics.Explain(r)}
}

type snapshotView struct {
	storage.DashboardSnapshot
	Report *reportView `json:"report"`
}

func explainSnapshot(s storage.DashboardSnapshot) snapshotView {
	v := snapshotView{DashboardSnapshot: s}
	if s.Report != nil {
		r := explainReport(*s.Report)
		v.Report = &r
	}
	return v
}
