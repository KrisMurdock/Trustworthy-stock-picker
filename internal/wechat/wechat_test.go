package wechat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/articlechart"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/notify"
)

func TestSendCreatesDraftWithDefaultCoverAndCachesCredentials(t *testing.T) {
	tokenCalls := 0
	coverCalls := 0
	draftCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cgi-bin/stable_token":
			tokenCalls++
			var payload map[string]string
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload["grant_type"] != "client_credential" || payload["appid"] != "appid" || payload["secret"] != "appsecret" {
				t.Fatalf("unexpected token payload: %+v", payload)
			}
			fmt.Fprint(w, `{"access_token":"token-1","expires_in":7200}`)
		case "/cgi-bin/material/add_material":
			coverCalls++
			if r.URL.Query().Get("access_token") != "token-1" || r.URL.Query().Get("type") != "image" {
				t.Fatalf("unexpected cover query: %s", r.URL.RawQuery)
			}
			if err := r.ParseMultipartForm(maxArticleBytes); err != nil {
				t.Fatal(err)
			}
			file, header, err := r.FormFile("media")
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			if header.Filename != "cover.png" || header.Size != int64(len(defaultCoverPNG)) {
				t.Fatalf("unexpected cover: %s %d", header.Filename, header.Size)
			}
			fmt.Fprint(w, `{"media_id":"cover-1"}`)
		case "/cgi-bin/draft/add":
			draftCalls++
			if r.URL.Query().Get("access_token") != "token-1" {
				t.Fatalf("unexpected draft token: %s", r.URL.RawQuery)
			}
			var payload draftRequest
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if len(payload.Articles) != 1 {
				t.Fatalf("articles = %d", len(payload.Articles))
			}
			article := payload.Articles[0]
			if article.Title != "观察标题" || article.ThumbMediaID != "cover-1" || article.Author != "Stock Watcher" ||
				article.Digest != "观察摘要" || article.NeedOpenComment != 0 || article.OnlyFansCanComment != 0 {
				t.Fatalf("unexpected article: %+v", article)
			}
			if !strings.Contains(article.Content, "<h2>先说结论</h2>") || !strings.Contains(article.Content, "&lt;不是标签&gt;") {
				t.Fatalf("unexpected content: %s", article.Content)
			}
			fmt.Fprint(w, `{"media_id":"draft-1"}`)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := newDraftClient("wechat-draft", "appid", "appsecret", "", "Stock Watcher", time.Second, server.URL)
	message := notify.Message{Article: &notify.Article{
		Title:  "观察标题",
		Digest: "观察摘要",
		Body:   "先说结论\n正文包含 <不是标签>。\n1. 候选一\n   缩进说明",
	}}
	if err := client.Send(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	if err := client.Send(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	if tokenCalls != 1 || coverCalls != 1 || draftCalls != 2 {
		t.Fatalf("calls token=%d cover=%d draft=%d", tokenCalls, coverCalls, draftCalls)
	}
}

func TestSendRefreshesInvalidTokenOnce(t *testing.T) {
	tokenCalls := 0
	draftCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cgi-bin/stable_token":
			tokenCalls++
			fmt.Fprintf(w, `{"access_token":"token-%d","expires_in":7200}`, tokenCalls)
		case "/cgi-bin/draft/add":
			draftCalls++
			if draftCalls == 1 {
				fmt.Fprint(w, `{"errcode":40001,"errmsg":"invalid credential"}`)
				return
			}
			if r.URL.Query().Get("access_token") != "token-2" {
				t.Fatalf("draft did not retry with refreshed token: %s", r.URL.RawQuery)
			}
			fmt.Fprint(w, `{"media_id":"draft-1"}`)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := newDraftClient("wechat-draft", "appid", "appsecret", "cover-fixed", "", time.Second, server.URL)
	if err := client.Send(context.Background(), articleMessage()); err != nil {
		t.Fatal(err)
	}
	if tokenCalls != 2 || draftCalls != 2 {
		t.Fatalf("calls token=%d draft=%d", tokenCalls, draftCalls)
	}
}

func TestSendClassifiesBusinessErrors(t *testing.T) {
	for _, test := range []struct {
		name      string
		code      int
		wantKind  notify.Kind
		wantRetry time.Duration
	}{
		{name: "unauthorized", code: 48001, wantKind: notify.Permanent},
		{name: "rate_limited", code: 45011, wantKind: notify.Retryable, wantRetry: time.Minute},
		{name: "ip_not_whitelisted", code: 40164, wantKind: notify.Permanent},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/cgi-bin/stable_token":
					fmt.Fprint(w, `{"access_token":"token-1","expires_in":7200}`)
				case "/cgi-bin/draft/add":
					fmt.Fprintf(w, `{"errcode":%d,"errmsg":"rejected"}`, test.code)
				default:
					t.Fatalf("unexpected path: %s", r.URL.Path)
				}
			}))
			defer server.Close()

			client := newDraftClient("wechat-draft", "appid", "appsecret", "cover-fixed", "", time.Second, server.URL)
			err := client.Send(context.Background(), articleMessage())
			var failure *notify.DeliveryError
			if !errors.As(err, &failure) {
				t.Fatalf("error = %T %v", err, err)
			}
			if failure.Kind != test.wantKind || failure.RetryAfter != test.wantRetry {
				t.Fatalf("failure = %+v", failure)
			}
			if strings.Contains(failure.Error(), "token-1") || strings.Contains(failure.Error(), "appsecret") {
				t.Fatalf("failure leaked credentials: %v", failure)
			}
		})
	}
}

func TestSendRejectsInvalidArticleBeforeCallingWeChat(t *testing.T) {
	client := newDraftClient("wechat-draft", "appid", "appsecret", "cover-fixed", "", time.Second, "https://example.invalid")
	for _, message := range []notify.Message{
		{},
		{Article: &notify.Article{Title: strings.Repeat("长", 33), Body: "正文"}},
		{Article: &notify.Article{Title: "标题", Body: " "}},
	} {
		err := client.Send(context.Background(), message)
		var failure *notify.DeliveryError
		if !errors.As(err, &failure) || failure.Kind != notify.Permanent {
			t.Fatalf("error = %T %v", err, err)
		}
	}
}

func articleMessage() notify.Message {
	return notify.Message{Article: &notify.Article{Title: "观察标题", Digest: "观察摘要", Body: "先说结论\n正文"}}
}

func TestSendUploadsInlineChartAndReusesCachedURL(t *testing.T) {
	tokenCalls, uploadCalls, draftCalls := 0, 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cgi-bin/stable_token":
			tokenCalls++
			fmt.Fprint(w, `{"access_token":"token-1","expires_in":7200}`)
		case "/cgi-bin/media/uploadimg":
			uploadCalls++
			if r.URL.Query().Get("access_token") != "token-1" {
				t.Fatalf("unexpected upload token: %s", r.URL.RawQuery)
			}
			if err := r.ParseMultipartForm(maxArticleBytes); err != nil {
				t.Fatal(err)
			}
			file, header, err := r.FormFile("media")
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			if header.Filename != "chart.png" {
				t.Fatalf("unexpected chart filename: %s", header.Filename)
			}
			signature := make([]byte, 8)
			if _, err := file.Read(signature); err != nil {
				t.Fatal(err)
			}
			if string(signature) != "\x89PNG\r\n\x1a\n" {
				t.Fatalf("uploaded chart is not PNG: %q", signature)
			}
			fmt.Fprint(w, `{"url":"https://mmbiz.qpic.cn/chart-1"}`)
		case "/cgi-bin/draft/add":
			draftCalls++
			var payload draftRequest
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			content := payload.Articles[0].Content
			if !strings.Contains(content, `<img src="https://mmbiz.qpic.cn/chart-1"`) ||
				!strings.Contains(content, "sz000001 日 K 结构图") {
				t.Fatalf("draft lost chart markup: %s", content)
			}
			fmt.Fprint(w, `{"media_id":"draft-1"}`)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := newDraftClient("wechat-draft", "appid", "appsecret", "cover-fixed", "", time.Second, server.URL)
	message := notify.Message{Article: &notify.Article{
		Title: "观察标题", Body: "先说结论\n\n[[chart:chart-sz000001]]\n",
		Charts: []articlechart.Spec{wechatTestChart()},
	}}
	if err := client.Send(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	if err := client.Send(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	if tokenCalls != 1 || uploadCalls != 1 || draftCalls != 2 {
		t.Fatalf("calls token=%d upload=%d draft=%d", tokenCalls, uploadCalls, draftCalls)
	}
}

func TestSendRejectsUnrenderableChartBeforeDraft(t *testing.T) {
	draftCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cgi-bin/stable_token":
			fmt.Fprint(w, `{"access_token":"token-1","expires_in":7200}`)
		case "/cgi-bin/draft/add":
			draftCalls++
			fmt.Fprint(w, `{"media_id":"draft-1"}`)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	chart := wechatTestChart()
	chart.Bars[0].High = chart.Bars[0].Low - 1
	client := newDraftClient("wechat-draft", "appid", "appsecret", "cover-fixed", "", time.Second, server.URL)
	err := client.Send(context.Background(), notify.Message{Article: &notify.Article{
		Title: "观察标题", Body: "[[chart:chart-sz000001]]\n", Charts: []articlechart.Spec{chart},
	}})
	var failure *notify.DeliveryError
	if !errors.As(err, &failure) || failure.Kind != notify.Permanent {
		t.Fatalf("error = %T %v", err, err)
	}
	if draftCalls != 0 {
		t.Fatalf("invalid chart reached draft/add: %d", draftCalls)
	}
}

func wechatTestChart() articlechart.Spec {
	bars := []articlechart.Bar{
		{Date: "2026-10-16", Open: 10, High: 10.4, Low: 9.8, Close: 10.2},
		{Date: "2026-10-17", Open: 10.2, High: 10.6, Low: 10.0, Close: 10.5},
		{Date: "2026-10-20", Open: 10.5, High: 10.8, Low: 10.3, Close: 10.6},
		{Date: "2026-10-21", Open: 10.6, High: 11.0, Low: 10.4, Close: 10.9},
	}
	return articlechart.Spec{
		Version: articlechart.Version, ID: "chart-sz000001", Code: "sz000001", AsOf: "2026-10-21", Adjust: "qfq",
		Caption: "sz000001 日 K 结构图", Bars: bars,
		Zone:   &articlechart.Zone{FromDate: bars[0].Date, ToDate: bars[2].Date, Upper: 10.6, Lower: 10.2},
		Levels: []articlechart.Level{{Kind: "support", Label: "SUP", Price: 10.2}},
	}
}
