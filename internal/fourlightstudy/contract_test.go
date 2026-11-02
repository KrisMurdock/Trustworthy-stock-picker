package fourlightstudy

import (
	"strings"
	"testing"
)

func TestManifestRejectsAlteredCalendarAndLeakingBounds(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Manifest)
	}{
		{"calendar_digest", func(m *Manifest) { m.Calendar.Digest = strings.Repeat("b", 64) }},
		{"calendar_day_removed", func(m *Manifest) { m.Calendar.Days = m.Calendar.Days[1:] }},
		{"calendar_source", func(m *Manifest) { m.Calendar.Source = "changed" }},
		{"observation_in_reserved", func(m *Manifest) { m.ObservationThrough = m.ReservedFrom }},
		{"signals_after_observation", func(m *Manifest) { m.Through = m.ReservedFrom }},
		{"reserved_after_asof", func(m *Manifest) { m.ReservedFrom = "2099-01-01" }},
		{"unknown_universe", func(m *Manifest) { m.UniversePolicy = "top_n" }},
		{"missing_parameters", func(m *Manifest) { m.Parameters.TradeCash = 0 }},
		{"missing_database_digest", func(m *Manifest) { m.DatabaseSHA256 = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := studyFixture(t)
			tc.change(&m)
			if err := m.Validate(); err == nil {
				t.Fatal("invalid manifest accepted")
			}
		})
	}
}
