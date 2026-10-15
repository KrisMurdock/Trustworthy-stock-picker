package market

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

func baoTestServer(t *testing.T, reply func(string, []string) (string, string)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
				reader := bufio.NewReader(conn)
				historyRequests := 0
				for {
					packet, err := reader.ReadString('\n')
					if err != nil {
						return
					}
					if len(packet) < 24 {
						t.Error("short BaoStock request")
						return
					}
					header := strings.Split(packet[:21], baoSeparator)
					n, err := strconv.Atoi(header[2])
					if err != nil || 21+n >= len(packet) {
						t.Error("invalid request length")
						return
					}
					checksum := strings.TrimSpace(packet[22+n:])
					want := strconv.FormatUint(uint64(crc32.ChecksumIEEE([]byte(packet[:21+n]))), 10)
					if checksum != want {
						t.Error("wrong outbound CRC")
						return
					}
					if header[1] == "95" {
						historyRequests++
						if historyRequests > 2 {
							t.Error("historical session was not rotated between years")
							return
						}
					}
					kind, body := reply(header[1], strings.Split(packet[21:21+n], baoSeparator))
					if kind == "close" {
						return
					}
					data := []byte(body)
					if kind == "96" {
						var compressed bytes.Buffer
						zw := zlib.NewWriter(&compressed)
						_, _ = zw.Write(data)
						_ = zw.Close()
						data = compressed.Bytes()
					}
					head := fmt.Sprintf("00.9.10\x01%s\x01%010d", kind, len(data))
					response := append([]byte(head), data...)
					response = append(response, []byte("\x011234<![CDATA[]]>\n")...)
					// Split frames across writes: TCP message boundaries are not packet boundaries.
					for len(response) > 0 {
						n := 7
						if len(response) < n {
							n = len(response)
						}
						if _, err := conn.Write(response[:n]); err != nil {
							return
						}
						response = response[n:]
					}
				}
			}()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		select {
		case <-done:
		case <-time.After(11 * time.Second):
			t.Error("BaoStock connection not released")
		}
	})
	return ln.Addr().String()
}

func baoReply(parts []string, rows [][]string) string {
	data, _ := json.Marshal(map[string]any{"record": rows})
	return strings.Join([]string{"0", "success", parts[0], parts[1], parts[2], parts[3], string(data), parts[4], parts[5], parts[6], parts[7], parts[8], parts[9]}, baoSeparator) + "\n"
}

func TestBaoStockLoginCompressedPaginationAndAdjustment(t *testing.T) {
	for _, adjust := range []string{"none", "qfq", "hfq"} {
		t.Run(adjust, func(t *testing.T) {
			pages := 0
			address := baoTestServer(t, func(kind string, parts []string) (string, string) {
				if kind == "00" {
					if strings.Join(parts, ",") != "login,anonymous,123456,0" {
						t.Error("unexpected login")
					}
					return "01", "0\x01success\x01login\x01anonymous\x0120261015"
				}
				pages++
				flag := map[string]string{"none": "3", "qfq": "2", "hfq": "1"}[adjust]
				if kind != "95" || len(parts) != 10 || parts[4] != "sh.688981" || parts[9] != flag {
					t.Error("invalid BaoStock query")
					return "close", ""
				}
				var rows [][]string
				last, _ := time.Parse("2006-01-02", parts[7])
				first, _ := time.Parse("2006-01-02", parts[6])
				for day := first; !day.After(last); day = day.AddDate(0, 0, 1) {
					if day.Weekday() != time.Saturday && day.Weekday() != time.Sunday {
						rows = append(rows, []string{day.Format("2006-01-02"), parts[4], "10", "12", "9", "11", "10000", "1", flag})
					}
				}
				page, _ := strconv.Atoi(parts[2])
				begin := (page - 1) * baoPageSize
				end := begin + baoPageSize
				if end > len(rows) {
					end = len(rows)
				}
				if begin > len(rows) {
					t.Error("extra page")
					return "close", ""
				}
				return "96", baoReply(parts, rows[begin:end])
			})
			c := NewBaoStockClient(time.Second, 0)
			c.Address = address
			h, err := c.Fetch(context.Background(), providerStock, "2016-10-13", "2026-10-13", adjust)
			if err != nil {
				t.Fatal(err)
			}
			if pages < 10 || len(h.Bars) < 2500 || h.Bars[0].Date != "2016-10-13" || h.Bars[len(h.Bars)-1].Date != "2026-10-13" || h.Bars[0].Volume != 10000 {
				t.Fatalf("lost long history: pages=%d bars=%d", pages, len(h.Bars))
			}
		})
	}
}

func TestBaoStockErrorsNeverReturnPartialHistory(t *testing.T) {
	for _, mode := range []string{"login_denied", "wrong_adjustment", "wrong_code", "malformed", "disconnect", "timeout", "cancel", "suspended"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			address := baoTestServer(t, func(kind string, parts []string) (string, string) {
				if kind == "00" {
					if mode == "login_denied" {
						return "01", "10001006\x01sensitive-upstream-error"
					}
					return "01", "0\x01success\x01login\x01anonymous"
				}
				if mode == "timeout" {
					time.Sleep(100 * time.Millisecond)
				}
				if mode == "cancel" {
					cancel()
					time.Sleep(10 * time.Millisecond)
				}
				if mode == "disconnect" {
					return "close", ""
				}
				if mode == "malformed" {
					return "96", "malformed"
				}
				row := []string{"2026-10-13", parts[4], "10", "12", "9", "11", "10000", "1", "3"}
				if mode == "wrong_adjustment" {
					row[8] = "2"
				}
				if mode == "wrong_code" {
					row[1] = "sz.000001"
				}
				if mode == "suspended" {
					row[7] = "0"
				}
				return "96", baoReply(parts, [][]string{row})
			})
			c := NewBaoStockClient(time.Second, 0)
			if mode == "timeout" {
				c.Timeout = 30 * time.Millisecond
			}
			c.Address = address
			h, err := c.Fetch(ctx, providerStock, "2026-10-12", "2026-10-13", "none")
			if err == nil || len(h.Bars) > 0 {
				t.Fatal("published failing/partial history")
			}
			if strings.Contains(err.Error(), "sensitive") {
				t.Fatal("leaked provider message")
			}
			if mode == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost cancellation: %v", err)
			}
			if (mode == "timeout" || mode == "disconnect") && IsIsolatable(err) {
				t.Fatal("network failure isolated")
			}
		})
	}
}

func TestBaoStockTruncatedCompressedFrame(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()
	go func() {
		defer server.Close()
		reader := bufio.NewReader(server)
		_, _ = reader.ReadString('\n')
		_, _ = io.WriteString(server, "00.9.10\x0196\x010000000030short")
	}()
	s := baoSession{conn: client, reader: bufio.NewReader(client), timeout: time.Second}
	if _, err := s.exchange(context.Background(), "95", "96", "query"); err == nil {
		t.Fatal("accepted truncated TCP frame")
	}
}
