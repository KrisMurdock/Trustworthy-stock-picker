package fourlightalign

import (
	"context"
	"encoding/csv"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
)

const maxReferenceRows = 200000

var requiredColumns = []string{"code", "date", "trend", "energy", "mid_term", "short_term"}

// ReadCSV reads a continuous target-software sample. Extra columns are ignored;
// required columns may appear in any order.
func ReadCSV(ctx context.Context, reader io.Reader) ([]Observation, error) {
	decoder := csv.NewReader(reader)
	decoder.FieldsPerRecord = -1
	decoder.TrimLeadingSpace = true
	header, err := decoder.Read()
	if err != nil {
		return nil, errors.New("reference CSV has no header")
	}
	columns := map[string]int{}
	for index, name := range header {
		name = normalizeColumn(name)
		if name == "" {
			continue
		}
		if _, exists := columns[name]; exists {
			return nil, errors.New("reference CSV has duplicate columns")
		}
		columns[name] = index
	}
	for _, name := range requiredColumns {
		if _, exists := columns[name]; !exists {
			return nil, errors.New("reference CSV is missing a required column")
		}
	}

	observations := []Observation{}
	seen := map[string]bool{}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		record, err := decoder.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, errors.New("reference CSV row is malformed")
		}
		if len(record) != len(header) {
			return nil, errors.New("reference CSV row has a different column count")
		}
		row := Observation{
			Code:      strings.ToLower(strings.TrimSpace(record[columns["code"]])),
			Date:      strings.TrimSpace(record[columns["date"]]),
			Trend:     normalizeLight(record[columns["trend"]]),
			Energy:    normalizeLight(record[columns["energy"]]),
			MidTerm:   normalizeLight(record[columns["mid_term"]]),
			ShortTerm: normalizeLight(record[columns["short_term"]]),
		}
		if !config.ValidCode(row.Code) {
			return nil, errors.New("reference CSV contains an invalid stock code")
		}
		parsed, err := time.Parse("2006-01-02", row.Date)
		if err != nil || parsed.Format("2006-01-02") != row.Date {
			return nil, errors.New("reference CSV contains an invalid date")
		}
		if err := validateObservation(row); err != nil {
			return nil, err
		}
		key := row.Code + "/" + row.Date
		if seen[key] {
			return nil, errors.New("reference CSV contains duplicate code/date rows")
		}
		seen[key] = true
		observations = append(observations, row)
		if len(observations) > maxReferenceRows {
			return nil, errors.New("reference CSV exceeds the row limit")
		}
	}
	if len(observations) == 0 {
		return nil, errors.New("reference CSV has no data rows")
	}
	return observations, nil
}

func normalizeColumn(value string) string {
	value = strings.TrimPrefix(strings.TrimSpace(value), "\ufeff")
	return strings.ToLower(value)
}

func normalizeLight(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "red", "r", "红":
		return "red"
	case "green", "g", "绿":
		return "green"
	case "unknown", "u", "未知", "灰":
		return "unknown"
	default:
		return value
	}
}
