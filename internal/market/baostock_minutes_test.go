package market

import (
	"context"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
)

func baoMinuteRows(t *testing.T, code string, days []string) [][]string {
	t.Helper()
	rows := make([][]string, 0, len(days)*48)
	for _, day := range days {
		date, err := time.ParseInLocation("2006-01-02", day, Shanghai)
		if err != nil {
			t.Fatal(err)
		}
		for _, start := range []int{570, 780} {
			for minute := start + 5; minute <= start+120; minute += 5 {
				end := time.Date(date.Year(), date.Month(), date.Day(), minute/60, minute%60, 0, 0, Shanghai)
				rows = append(rows, []string{
					day, end.Format("20060102150405") + "000", code,
					"10.0000", "10.2000", "9.9000", "10.1000", "10000", "100500.0000", "3",
				})
			}
		}
	}
	return rows
}

func TestBaoStockMinuteRangePaginationAndVolumeUnit(t *testing.T) {
	days := []string{"2026-09-01", "2026-09-02", "2026-09-03", "2026-09-04", "2026-09-07", "2026-09-08", "2026-09-09", "2026-09-10", "2026-09-11", "2026-09-14"}
	allRows := baoMinuteRows(t, "sh.600036", days)
	address := baoTestServer(t, func(kind string, parts []string) (string, string) {
		if kind == "00" {
			return "01", "0\x01success\x01login\x01anonymous\x0120261015"
		}
		if len(parts) != 10 || parts[4] != "sh.600036" || parts[8] != "5" || parts[9] != "3" {
			t.Error("unexpected minute query")
			return "close", ""
		}
		filtered := make([][]string, 0, len(allRows))
		for _, row := range allRows {
			if row[0] >= parts[6] && row[0] <= parts[7] {
				filtered = append(filtered, row)
			}
		}
		page, pageErr := strconv.Atoi(parts[2])
		pageSize, sizeErr := strconv.Atoi(parts[3])
		if pageErr != nil || sizeErr != nil || page < 1 || pageSize != baoMinutePageSize {
			t.Errorf("unexpected minute pagination page=%q size=%q", parts[2], parts[3])
			return "close", ""
		}
		begin := (page - 1) * pageSize
		end := begin + pageSize
		if begin >= len(filtered) {
			return "96", baoReply(parts, nil)
		}
		if end > len(filtered) {
			end = len(filtered)
		}
		return "96", baoReply(parts, filtered[begin:end])
	})
	client := NewBaoStockClient(time.Second, 0)
	client.Address = address
	archive, err := client.FetchMinuteRange(context.Background(), config.Stock{Code: "sh600036", Name: "招商银行"}, "2026-09-01", "2026-09-30")
	if err != nil {
		t.Fatal(err)
	}
	if len(archive.Bars) != len(allRows) || archive.Source != "baostock" || archive.VolumeUnit != "shares" {
		t.Fatalf("unexpected archive: %+v", archive)
	}
	if !archive.Bars[0].End.Equal(time.Date(2026, 9, 1, 9, 35, 0, 0, Shanghai)) ||
		!archive.Bars[47].End.Equal(time.Date(2026, 9, 1, 15, 0, 0, 0, Shanghai)) ||
		archive.Bars[0].Volume != 10000 || !archive.Bars[0].Final {
		t.Fatalf("unexpected bars: first=%+v last=%+v", archive.Bars[0], archive.Bars[47])
	}
	if got := TencentMinuteVolumeRaw("sh600036", 10000); got != 100 {
		t.Fatalf("main-board volume = %v", got)
	}
	if got := TencentMinuteVolumeRaw("sh688519", 10000); got != 10000 {
		t.Fatalf("STAR volume = %v", got)
	}
}

func TestBaoStockMinuteRangeRejectsMalformedRows(t *testing.T) {
	address := baoTestServer(t, func(kind string, parts []string) (string, string) {
		if kind == "00" {
			return "01", "0\x01success\x01login\x01anonymous"
		}
		row := []string{"2026-09-01", "20260901093500000", "sh.600036", "10", "9", "9.5", "10.1", "100", "1000", "3"}
		return "96", baoReply(parts, [][]string{row})
	})
	client := NewBaoStockClient(time.Second, 0)
	client.Address = address
	if _, err := client.FetchMinuteRange(context.Background(), config.Stock{Code: "sh600036"}, "2026-09-01", "2026-09-30"); err == nil {
		t.Fatal("accepted impossible high/low")
	}
}

func TestBaoStockMinuteRetriesWithFreshSession(t *testing.T) {
	var requests atomic.Int32
	address := baoTestServer(t, func(kind string, parts []string) (string, string) {
		if kind == "00" {
			return "01", "0\x01success\x01login\x01anonymous"
		}
		if requests.Add(1) == 1 {
			return "close", ""
		}
		rows := baoMinuteRows(t, "sh.600036", []string{"2026-09-01"})
		return "96", baoReply(parts, rows)
	})
	client := NewBaoStockClient(time.Second, 0)
	client.Address = address
	archive, err := client.FetchMinuteRange(context.Background(), config.Stock{Code: "sh600036"}, "2026-09-01", "2026-09-02")
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 || len(archive.Bars) != 48 {
		t.Fatalf("requests=%d bars=%d", requests.Load(), len(archive.Bars))
	}
}
