package market

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"context"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"stock-watcher/internal/config"
)

const baoSeparator = "\x01"

// Small frames avoid truncated large responses observed on the public endpoint.
const baoPageSize = 250

const baoFields = "date,code,open,high,low,close,volume,tradestatus,adjustflag"

// BaoStockClient implements the official 0.9.4 wire protocol in Go. Each yearly
// window owns a session; cancellation closes its socket without affecting others.
type BaoStockClient struct {
	Address string
	Timeout time.Duration
	pacer   requestPacer
}

func NewBaoStockClient(timeout, interval time.Duration) *BaoStockClient {
	return &BaoStockClient{Address: "public-api.baostock.com:10030", Timeout: timeout, pacer: requestPacer{interval: interval}}
}
func (c *BaoStockClient) Close() {}

type baoSession struct {
	conn    net.Conn
	reader  *bufio.Reader
	timeout time.Duration
}

func (s *baoSession) exchange(ctx context.Context, kind, responseKind, body string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(s.timeout)
	if end, ok := ctx.Deadline(); ok && end.Before(deadline) {
		deadline = end
	}
	if err := s.conn.SetDeadline(deadline); err != nil {
		return nil, &Failure{Kind: "baostock_connection"}
	}
	header := fmt.Sprintf("00.9.40\x01%s\x01%010d", kind, len(body))
	message := header + body
	packet := fmt.Sprintf("%s\x01%d\n", message, crc32.ChecksumIEEE([]byte(message)))
	if _, err := io.Copy(s.conn, strings.NewReader(packet)); err != nil {
		return nil, baoNetworkError(ctx)
	}
	var head [21]byte
	if _, err := io.ReadFull(s.reader, head[:]); err != nil {
		return nil, baoNetworkError(ctx)
	}
	parts := strings.Split(string(head[:]), baoSeparator)
	if len(parts) != 3 || parts[1] != responseKind {
		return nil, &DataError{Reason: "invalid_baostock_header"}
	}
	size, err := strconv.Atoi(parts[2])
	if err != nil || size < 1 || size > 8<<20 {
		return nil, &DataError{Reason: "invalid_baostock_length"}
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(s.reader, data); err != nil {
		return nil, baoNetworkError(ctx)
	}
	// The trailer can contain a checksum before the fixed terminator. Bound it
	// independently; never scan unbounded data or stop at a newline inside zlib.
	trailer := make([]byte, 0, 128)
	for !bytes.HasSuffix(trailer, []byte("<![CDATA[]]>\n")) {
		if len(trailer) >= 128 {
			return nil, &DataError{Reason: "invalid_baostock_trailer"}
		}
		b, err := s.reader.ReadByte()
		if err != nil {
			return nil, baoNetworkError(ctx)
		}
		trailer = append(trailer, b)
	}
	if responseKind == "96" {
		zr, err := zlib.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, &DataError{Reason: "invalid_baostock_compression"}
		}
		data, err = io.ReadAll(io.LimitReader(zr, (8<<20)+1))
		_ = zr.Close()
		if err != nil || len(data) > 8<<20 {
			return nil, &DataError{Reason: "invalid_baostock_compression"}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fields := strings.Split(strings.TrimSuffix(string(data), "\n"), baoSeparator)
	if len(fields) < 2 {
		return nil, &DataError{Reason: "invalid_baostock_envelope"}
	}
	if fields[0] != "0" {
		status := 400
		switch fields[0] {
		case "10001001", "10001002", "10001006", "10001007", "10001011":
			status = 403
		case "10001005":
			status = 429
		case "10002001", "10002002", "10002003":
			status = 503
		}
		return nil, &Failure{Status: status, Kind: "baostock_api"}
	}
	return fields, nil
}

func baoNetworkError(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return &Failure{Kind: "baostock_network"}
}

func (c *BaoStockClient) Fetch(ctx context.Context, stock config.Stock, start, end, adjust string) (History, error) {
	h, probe, err := requestHistory(stock, start, end, adjust, "baostock")
	if err != nil {
		return h, err
	}
	flag := map[string]string{"none": "3", "qfq": "2", "hfq": "1"}[adjust]
	code := stock.Code[:2] + "." + stock.Code[2:]
	first, _ := time.Parse("2006-01-02", probe)
	last, _ := time.Parse("2006-01-02", end)
	// Each year owns a short-lived session. The public service can stop
	// answering repeated historical queries on a long-lived connection.
	for from := first; !from.After(last); {
		to := time.Date(from.Year(), 12, 31, 0, 0, 0, 0, time.UTC)
		if to.After(last) {
			to = last
		}
		bars, err := c.fetchYear(ctx, code, flag, from.Format("2006-01-02"), to.Format("2006-01-02"))
		if err != nil {
			return History{}, err
		}
		h.Bars = append(h.Bars, bars...)
		if len(h.Bars) > MaxHistoryBars {
			return History{}, &DataError{Reason: "baostock_history_exceeds_limit"}
		}
		from = to.AddDate(0, 0, 1)
	}
	return finishHistory(h, start)
}

func (c *BaoStockClient) fetchYear(ctx context.Context, code, flag, pageStart, pageEnd string) ([]Bar, error) {
	if err := c.pacer.wait(ctx); err != nil {
		return nil, err
	}
	conn, err := (&net.Dialer{Timeout: c.Timeout}).DialContext(ctx, "tcp", c.Address)
	if err != nil {
		return nil, baoNetworkError(ctx)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	s := baoSession{conn: conn, reader: bufio.NewReader(conn), timeout: c.Timeout}
	login, err := s.exchange(ctx, "00", "01", strings.Join([]string{"login", "anonymous", "123456", "0"}, baoSeparator))
	if err != nil {
		return nil, err
	}
	if len(login) < 4 || login[2] != "login" || login[3] != "anonymous" {
		return nil, &DataError{Reason: "invalid_baostock_login"}
	}
	var bars []Bar
	complete := false
	for page := 1; page <= 2; page++ {
		if err := c.pacer.wait(ctx); err != nil {
			return nil, err
		}
		body := strings.Join([]string{"query_history_k_data_plus", "anonymous", strconv.Itoa(page), strconv.Itoa(baoPageSize), code, baoFields, pageStart, pageEnd, "d", flag}, baoSeparator)
		parts, err := s.exchange(ctx, "95", "96", body)
		if err != nil {
			return nil, fmt.Errorf("baostock year %s page %d: %w", pageStart[:4], page, err)
		}
		if len(parts) < 13 || parts[2] != "query_history_k_data_plus" || parts[3] != "anonymous" || parts[4] != strconv.Itoa(page) || parts[5] != strconv.Itoa(baoPageSize) || parts[7] != code || parts[8] != baoFields || parts[9] != pageStart || parts[10] != pageEnd || parts[11] != "d" || parts[12] != flag {
			return nil, &DataError{Reason: "baostock_query_mismatch"}
		}
		var data struct {
			Records [][]string `json:"record"`
		}
		if parts[6] != "" && json.Unmarshal([]byte(parts[6]), &data) != nil {
			return nil, &DataError{Reason: "invalid_baostock_records"}
		}
		if len(data.Records) > baoPageSize {
			return nil, &DataError{Reason: "invalid_baostock_page_size"}
		}
		for _, row := range data.Records {
			if len(row) != 9 || row[1] != code || row[0] < pageStart || row[0] > pageEnd || row[8] != flag {
				return nil, &DataError{Reason: "invalid_baostock_row"}
			}
			// Suspended sessions have synthetic prices and zero volume. Exclude
			// them so the existing gap/staleness gate also applies to this source.
			if row[7] == "0" {
				continue
			}
			if row[7] != "1" {
				return nil, &DataError{Reason: "invalid_trade_status"}
			}
			bar := Bar{Date: row[0]}
			for i, dest := range []*float64{&bar.Open, &bar.High, &bar.Low, &bar.Close, &bar.Volume} {
				*dest, err = numeric(json.RawMessage(row[i+2]))
				if err != nil {
					return nil, err
				}
			}
			bars = append(bars, bar)
		}
		if len(data.Records) < baoPageSize {
			complete = true
			break
		}
	}
	if !complete {
		return nil, &DataError{Reason: "baostock_history_exceeds_limit"}
	}
	return bars, nil
}

func (c *BaoStockClient) FetchWindow(ctx context.Context, stock config.Stock, start, end, adjust string, count int) (History, error) {
	return recentWindow(ctx, c, stock, start, end, adjust, count)
}
