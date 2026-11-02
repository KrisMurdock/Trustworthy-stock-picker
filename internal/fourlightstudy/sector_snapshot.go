package fourlightstudy

import (
	"encoding/hex"
	"errors"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
)

// SectorSnapshot freezes a single published classification vintage, not a
// history of membership changes. SnapshotAsOf is the report period end, never
// an inferred effective date. ObservedAt records retrieval, not availability.
type SectorSnapshot struct {
	Version      int                `json:"schema_version"`
	Basis        string             `json:"basis"`
	Taxonomy     string             `json:"taxonomy"`
	Level        string             `json:"level"`
	SnapshotAsOf string             `json:"snapshot_as_of"`
	PublishedOn  string             `json:"published_on,omitempty"`
	ObservedAt   string             `json:"observed_at"`
	Sources      []SectorSource     `json:"sources"`
	Memberships  []SectorMembership `json:"memberships"`
}

type SectorSource struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

type SectorMembership struct {
	Code       string `json:"code"`
	SectorCode string `json:"sector_code"`
	SectorName string `json:"sector_name"`
}

func (s SectorSnapshot) Validate() error {
	if s.Version != 1 || (s.Basis != "published_snapshot" && s.Basis != "current_only") {
		return errors.New("unsupported sector snapshot version or basis")
	}
	if !sectorText(s.Taxonomy, 256) || !sectorText(s.Level, 128) || !snapshotDateValid(s.SnapshotAsOf) {
		return errors.New("sector snapshot requires taxonomy, level and report period end")
	}
	if s.Basis == "published_snapshot" {
		if !snapshotDateValid(s.PublishedOn) || s.PublishedOn < s.SnapshotAsOf {
			return errors.New("published classification requires a publication date on or after its report period end")
		}
	} else if s.PublishedOn != "" {
		return errors.New("current-only classification cannot claim historical publication")
	}
	observed, err := time.Parse(time.RFC3339Nano, s.ObservedAt)
	if err != nil || observed.Format(time.DateOnly) < s.SnapshotAsOf || observed.Format(time.DateOnly) < s.PublishedOn {
		return errors.New("sector observed_at must be an RFC3339 timestamp after the classification and publication dates")
	}
	if len(s.Sources) == 0 || len(s.Sources) > 32 {
		return errors.New("sector snapshot requires 1 to 32 frozen sources")
	}
	seen := map[string]bool{}
	for _, source := range s.Sources {
		u, err := url.Parse(source.URL)
		digest, hashErr := hex.DecodeString(source.SHA256)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || len(source.URL) > 4096 ||
			hashErr != nil || len(digest) != 32 || strings.ToLower(source.SHA256) != source.SHA256 || seen[source.URL] {
			return errors.New("sector sources require unique HTTPS URLs without credentials and lowercase SHA256 digests")
		}
		seen[source.URL] = true
	}
	if len(s.Memberships) == 0 || len(s.Memberships) > 20000 {
		return errors.New("sector snapshot requires 1 to 20000 memberships")
	}
	names := map[string]string{}
	for i, member := range s.Memberships {
		if !config.ValidCode(member.Code) || (i > 0 && member.Code <= s.Memberships[i-1].Code) ||
			!sectorText(member.SectorCode, 64) || !sectorText(member.SectorName, 256) {
			return errors.New("sector memberships require strictly sorted unique stock codes and nonempty industry codes and names")
		}
		if name, exists := names[member.SectorCode]; exists && name != member.SectorName {
			return errors.New("sector code has inconsistent names")
		}
		names[member.SectorCode] = member.SectorName
	}
	return nil
}

func sectorText(value string, limit int) bool {
	return value != "" && len(value) <= limit && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\r\n\t")
}

// Lookup assumes Validate succeeded once. A date-only publication is usable
// strictly after that date, avoiding an invented before-close publication time.
// current_only membership remains descriptive; callers must not use it to
// construct historical candidate groups.
func (s SectorSnapshot) Lookup(code, date string) (SectorMembership, string) {
	if !snapshotDateValid(date) {
		return SectorMembership{}, "invalid_signal_date"
	}
	if s.Basis == "published_snapshot" && date <= s.PublishedOn {
		return SectorMembership{}, "classification_not_yet_public"
	}
	i := sort.Search(len(s.Memberships), func(i int) bool { return s.Memberships[i].Code >= code })
	if i == len(s.Memberships) || s.Memberships[i].Code != code {
		return SectorMembership{}, "missing_membership"
	}
	return s.Memberships[i], ""
}
