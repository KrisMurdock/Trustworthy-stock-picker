package dashboard

import (
	"stock-watcher/internal/diagnostics"
	"stock-watcher/internal/selection"
	"stock-watcher/internal/storage"
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
