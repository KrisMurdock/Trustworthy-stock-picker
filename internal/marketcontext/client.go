package marketcontext

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

const (
	boardPageSize = 100
	maxBoards     = 5000
	maxBodyBytes  = 16 << 20
)

var boardCodePattern = regexp.MustCompile(`^BK[0-9]{4}$`)

type Board struct {
	Code     string
	Name     string
	Category string
}

type Client struct {
	HTTP            *http.Client
	ListEndpoint    string
	KlineEndpoint   string
	TencentEndpoint string
	HistoryLimit    int
	Now             func() time.Time
	Pacer           *pacer
}

type pacer struct {
	mu       sync.Mutex
	next     time.Time
	interval time.Duration
}

func (p *pacer) wait(ctx context.Context) error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	at := p.next
	if at.Before(time.Now()) {
		at = time.Now()
	}
	p.next = at.Add(p.interval)
	p.mu.Unlock()
	timer := time.NewTimer(time.Until(at))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}

func NewClient(c config.MarketContext, timeout time.Duration) *Client {
	interval, _ := time.ParseDuration(c.RequestInterval)
	if interval < 350*time.Millisecond {
		interval = 350 * time.Millisecond
	}
	return &Client{
		HTTP:            &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		ListEndpoint:    c.EastmoneyListEndpoint,
		KlineEndpoint:   c.EastmoneyKlineEndpoint,
		TencentEndpoint: c.TencentEndpoint,
		HistoryLimit:    c.HistoryLimit,
		Now:             time.Now,
		Pacer:           &pacer{interval: interval},
	}
}

func (c *Client) Close() {
	if c != nil && c.HTTP != nil {
		c.HTTP.CloseIdleConnections()
	}
}

// DiscoverBoards returns every industry and concept board exposed by the
// upstream list API. Category is preserved as a comma-separated set because a
// board can appear in both lists.
func (c *Client) DiscoverBoards(ctx context.Context) ([]Board, error) {
	merged := map[string]Board{}
	for _, definition := range []struct {
		category string
		fs       string
	}{
		{category: "industry", fs: "m:90+t:2"},
		{category: "concept", fs: "m:90+t:3"},
	} {
		total := 0
		for page := 1; ; page++ {
			if page > maxBoards/boardPageSize+2 {
				return nil, errors.New("board list exceeded page limit")
			}
			items, count, err := c.listPage(ctx, definition.fs, page)
			if err != nil {
				return nil, err
			}
			if page == 1 {
				total = count
				if total < 1 || total > maxBoards {
					return nil, errors.New("board list returned an invalid total")
				}
			}
			if len(items) == 0 {
				break
			}
			for _, item := range items {
				if !boardCodePattern.MatchString(item.Code) || strings.TrimSpace(item.Name) == "" {
					return nil, errors.New("board list contained an invalid board")
				}
				if existing, ok := merged[item.Code]; ok {
					if existing.Category != definition.category {
						existing.Category = "industry,concept"
					}
					merged[item.Code] = existing
					continue
				}
				merged[item.Code] = Board{Code: item.Code, Name: item.Name, Category: definition.category}
			}
			if page*boardPageSize >= total {
				break
			}
		}
	}
	if len(merged) == 0 {
		return nil, errors.New("board list returned no boards")
	}
	out := make([]Board, 0, len(merged))
	for _, item := range merged {
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out, nil
}

func (c *Client) listPage(ctx context.Context, fs string, page int) ([]Board, int, error) {
	u, err := url.Parse(c.ListEndpoint)
	if err != nil {
		return nil, 0, errors.New("invalid board list endpoint")
	}
	q := u.Query()
	q.Set("pn", strconv.Itoa(page))
	q.Set("pz", strconv.Itoa(boardPageSize))
	q.Set("po", "1")
	q.Set("np", "1")
	q.Set("fltt", "2")
	q.Set("invt", "2")
	q.Set("fid", "f3")
	q.Set("fs", fs)
	q.Set("fields", "f12,f14")
	u.RawQuery = q.Encode()
	body, err := c.get(ctx, u)
	if err != nil {
		return nil, 0, err
	}
	var envelope struct {
		RC   *int `json:"rc"`
		Data *struct {
			Total *int            `json:"total"`
			Diff  json.RawMessage `json:"diff"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.RC == nil || *envelope.RC != 0 || envelope.Data == nil || envelope.Data.Total == nil {
		return nil, 0, errors.New("invalid board list response")
	}
	var rows []struct {
		Code string `json:"f12"`
		Name string `json:"f14"`
	}
	if len(envelope.Data.Diff) > 0 && envelope.Data.Diff[0] == '[' {
		if err := json.Unmarshal(envelope.Data.Diff, &rows); err != nil {
			return nil, 0, errors.New("invalid board list rows")
		}
	} else {
		var indexed map[string]struct {
			Code string `json:"f12"`
			Name string `json:"f14"`
		}
		if err := json.Unmarshal(envelope.Data.Diff, &indexed); err != nil {
			return nil, 0, errors.New("invalid board list rows")
		}
		for _, row := range indexed {
			rows = append(rows, row)
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].Code < rows[j].Code })
	}
	out := make([]Board, 0, len(rows))
	for _, row := range rows {
		out = append(out, Board{Code: row.Code, Name: row.Name})
	}
	return out, *envelope.Data.Total, nil
}

// FetchBoard returns board history through asOf. When from is non-empty the
// caller already has older history and only the overlapping tail is requested.
func (c *Client) FetchBoard(ctx context.Context, board Board, asOf, from string) ([]storage.ContextBar, string, error) {
	if !boardCodePattern.MatchString(board.Code) {
		return nil, "", errors.New("invalid board code")
	}
	u, err := url.Parse(c.KlineEndpoint)
	if err != nil {
		return nil, "", errors.New("invalid board kline endpoint")
	}
	limit := c.HistoryLimit
	if limit < 640 || limit > 10000 {
		limit = 10000
	}
	q := u.Query()
	q.Set("secid", "90."+board.Code)
	q.Set("fields1", "f1,f2,f3,f4,f5,f6")
	q.Set("fields2", "f51,f52,f53,f54,f55,f56,f57,f58")
	q.Set("klt", "101")
	q.Set("fqt", "0")
	if from != "" {
		start, err := time.Parse("2006-01-02", from)
		if err != nil {
			return nil, "", errors.New("invalid board history start")
		}
		q.Set("beg", start.AddDate(0, 0, -3).Format("20060102"))
	} else {
		q.Set("beg", "0")
	}
	q.Set("end", strings.ReplaceAll(asOf, "-", ""))
	q.Set("lmt", strconv.Itoa(limit))
	u.RawQuery = q.Encode()
	body, err := c.get(ctx, u)
	if err != nil {
		return nil, "", err
	}
	var envelope struct {
		RC   *int `json:"rc"`
		Data *struct {
			Code   string   `json:"code"`
			Market *int     `json:"market"`
			Name   string   `json:"name"`
			Klines []string `json:"klines"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.RC == nil || *envelope.RC != 0 || envelope.Data == nil {
		return nil, "", errors.New("invalid board kline response")
	}
	data := envelope.Data
	if data.Code != board.Code || data.Market == nil || *data.Market != 90 {
		return nil, "", errors.New("board kline identity mismatch")
	}
	if len(data.Klines) > limit+1 {
		return nil, "", errors.New("board kline response exceeded limit")
	}
	bars, err := parseBoardRows(board.Code, data.Klines, asOf)
	if err != nil {
		return nil, "", err
	}
	name := data.Name
	if name == "" {
		name = board.Name
	}
	return bars, name, nil
}

func parseBoardRows(code string, rows []string, asOf string) ([]storage.ContextBar, error) {
	out := make([]storage.ContextBar, 0, len(rows))
	previous := ""
	for _, line := range rows {
		fields := strings.Split(line, ",")
		if len(fields) < 7 || fields[0] > asOf {
			continue
		}
		if _, err := time.Parse("2006-01-02", fields[0]); err != nil || fields[0] <= previous {
			return nil, errors.New("invalid board kline date")
		}
		values := make([]float64, 6)
		for i := range values {
			value, err := strconv.ParseFloat(strings.TrimSpace(fields[i+1]), 64)
			if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
				return nil, errors.New("invalid board kline numeric value")
			}
			values[i] = value
		}
		bar := storage.ContextBar{Code: code, Date: fields[0], Open: values[0], Close: values[1], High: values[2], Low: values[3], Volume: values[4], Amount: values[5]}
		if bar.Open <= 0 || bar.Close <= 0 || bar.Low <= 0 || bar.High < math.Max(bar.Open, bar.Close) || bar.Low > math.Min(bar.Open, bar.Close) {
			return nil, errors.New("invalid board kline OHLC range")
		}
		previous = bar.Date
		out = append(out, bar)
	}
	return out, nil
}

func (c *Client) FetchIndex(ctx context.Context, code, asOf string) ([]storage.ContextBar, string, error) {
	if indexOrder(code) >= 100 {
		return nil, "", errors.New("unsupported index code")
	}
	u, err := url.Parse(c.TencentEndpoint)
	if err != nil {
		return nil, "", errors.New("invalid index endpoint")
	}
	start, _ := time.Parse("2006-01-02", asOf)
	q := u.Query()
	q.Set("param", strings.Join([]string{code, "day", start.AddDate(-4, 0, 0).Format("2006-01-02"), asOf, "640", "qfq"}, ","))
	u.RawQuery = q.Encode()
	body, err := c.get(ctx, u)
	if err != nil {
		return nil, "", err
	}
	var envelope struct {
		Code *int                       `json:"code"`
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.Code == nil || *envelope.Code != 0 || envelope.Data == nil {
		return nil, "", errors.New("invalid index response")
	}
	raw, ok := envelope.Data[code]
	if !ok {
		return nil, "", errors.New("index response code mismatch")
	}
	var stock map[string]json.RawMessage
	if err := json.Unmarshal(raw, &stock); err != nil {
		return nil, "", errors.New("invalid index payload")
	}
	key := "qfqday"
	if _, ok := stock[key]; !ok {
		key = "day"
	}
	var rows [][]json.RawMessage
	if err := json.Unmarshal(stock[key], &rows); err != nil || len(rows) == 0 {
		return nil, "", errors.New("missing index daily rows")
	}
	out := make([]storage.ContextBar, 0, len(rows))
	previous := ""
	for _, row := range rows {
		if len(row) < 6 {
			return nil, "", errors.New("invalid index daily row")
		}
		var date string
		if json.Unmarshal(row[0], &date) != nil || date > asOf {
			continue
		}
		if _, err := time.Parse("2006-01-02", date); err != nil || date <= previous {
			return nil, "", errors.New("invalid index daily date")
		}
		values := make([]float64, 5)
		for i := range values {
			var text string
			if json.Unmarshal(row[i+1], &text) != nil {
				text = string(row[i+1])
			}
			value, err := strconv.ParseFloat(text, 64)
			if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
				return nil, "", errors.New("invalid index numeric value")
			}
			values[i] = value
		}
		bar := storage.ContextBar{Code: code, Date: date, Open: values[0], Close: values[1], High: values[2], Low: values[3], Volume: values[4]}
		if bar.Open <= 0 || bar.Close <= 0 || bar.Low <= 0 || bar.High < math.Max(bar.Open, bar.Close) || bar.Low > math.Min(bar.Open, bar.Close) {
			return nil, "", errors.New("invalid index OHLC range")
		}
		previous = bar.Date
		out = append(out, bar)
	}
	name := ""
	var qt map[string][]json.RawMessage
	if json.Unmarshal(stock["qt"], &qt) == nil && len(qt[code]) > 1 {
		_ = json.Unmarshal(qt[code][1], &name)
	}
	if len(out) == 0 {
		return nil, name, errors.New("no index data through requested date")
	}
	return out, name, nil
}

func (c *Client) get(ctx context.Context, u *url.URL) ([]byte, error) {
	if u == nil || u.Hostname() == "" || u.User != nil || u.RawQuery == "" {
		return nil, errors.New("invalid context endpoint")
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, errors.New("invalid context request")
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 stock-watcher/1.0")
		if err := c.Pacer.wait(ctx); err != nil {
			return nil, err
		}
		resp, err := c.HTTP.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = errors.New("context network request failed")
		} else {
			if resp.StatusCode == http.StatusOK {
				body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
				resp.Body.Close()
				if readErr != nil {
					lastErr = errors.New("cannot read context response")
				} else if len(body) > maxBodyBytes {
					return nil, errors.New("context response exceeded size limit")
				} else {
					return body, nil
				}
			} else {
				status := resp.StatusCode
				resp.Body.Close()
				lastErr = fmt.Errorf("context upstream returned HTTP %d", status)
				if status != http.StatusTooManyRequests && status < 500 {
					return nil, lastErr
				}
			}
		}
		if attempt < 2 {
			timer := time.NewTimer(time.Duration(attempt+1) * 200 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
	}
	return nil, lastErr
}
