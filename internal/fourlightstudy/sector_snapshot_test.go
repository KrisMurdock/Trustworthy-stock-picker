package fourlightstudy

import (
	"strings"
	"testing"
)

func testSectorSnapshot() SectorSnapshot {
	return SectorSnapshot{Version: 1, Basis: "published_snapshot", Taxonomy: "fixture", Level: "division",
		SnapshotAsOf: "2025-12-31", PublishedOn: "2026-04-03", ObservedAt: "2026-11-02T10:00:00+08:00",
		Sources:     []SectorSource{{URL: "https://example.org/classification.pdf", SHA256: strings.Repeat("a", 64)}},
		Memberships: []SectorMembership{{Code: "sh600000", SectorCode: "J66", SectorName: "货币金融服务"}, {Code: "sz000001", SectorCode: "J66", SectorName: "货币金融服务"}}}
}

func TestSectorSnapshotAvailabilityAndUnknown(t *testing.T) {
	s := testSectorSnapshot()
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, date := range []string{"2026-04-02", "2026-04-03"} {
		if member, reason := s.Lookup("sh600000", date); reason != "classification_not_yet_public" || member.Code != "" {
			t.Fatalf("invented availability on %s: %+v %s", date, member, reason)
		}
	}
	if member, reason := s.Lookup("sz000001", "2026-04-08"); reason != "" || member.SectorCode != "J66" {
		t.Fatalf("missing published classification: %+v %s", member, reason)
	}
	if _, reason := s.Lookup("sz000002", "2026-04-08"); reason != "missing_membership" {
		t.Fatal(reason)
	}
	if _, reason := s.Lookup("sh600000", "not-a-date"); reason != "invalid_signal_date" {
		t.Fatal(reason)
	}
	s.Basis, s.PublishedOn = "current_only", ""
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if member, reason := s.Lookup("sh600000", "2026-01-01"); reason != "" || member.Code == "" {
		t.Fatal("current-only mapping must remain available for descriptive audit")
	}
}

func TestSectorSnapshotRejectsUnverifiableContract(t *testing.T) {
	cases := map[string]func(*SectorSnapshot){
		"version":                    func(s *SectorSnapshot) { s.Version = 2 },
		"basis":                      func(s *SectorSnapshot) { s.Basis = "dated" },
		"taxonomy":                   func(s *SectorSnapshot) { s.Taxonomy = "" },
		"period":                     func(s *SectorSnapshot) { s.SnapshotAsOf = "2025-H2" },
		"missing publication":        func(s *SectorSnapshot) { s.PublishedOn = "" },
		"early publication":          func(s *SectorSnapshot) { s.PublishedOn = "2025-01-01" },
		"current claims publication": func(s *SectorSnapshot) { s.Basis = "current_only" },
		"observation":                func(s *SectorSnapshot) { s.ObservedAt = "2026-01-01T00:00:00Z" },
		"missing source":             func(s *SectorSnapshot) { s.Sources = nil },
		"credentials":                func(s *SectorSnapshot) { s.Sources[0].URL = "https://user:pass@example.org/file" },
		"bad digest":                 func(s *SectorSnapshot) { s.Sources[0].SHA256 = strings.Repeat("A", 64) },
		"duplicate source":           func(s *SectorSnapshot) { s.Sources = append(s.Sources, s.Sources[0]) },
		"empty":                      func(s *SectorSnapshot) { s.Memberships = nil },
		"duplicates":                 func(s *SectorSnapshot) { s.Memberships[1] = s.Memberships[0] },
		"order":                      func(s *SectorSnapshot) { s.Memberships[0], s.Memberships[1] = s.Memberships[1], s.Memberships[0] },
		"names":                      func(s *SectorSnapshot) { s.Memberships[1].SectorName = "另一名称" },
		"invalid stock":              func(s *SectorSnapshot) { s.Memberships[0].Code = "600000" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := testSectorSnapshot()
			mutate(&s)
			if err := s.Validate(); err == nil {
				t.Fatal("accepted invalid classification")
			}
		})
	}
}
