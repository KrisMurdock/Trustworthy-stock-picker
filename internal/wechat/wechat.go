// Package wechat creates Official Account drafts without publishing them.
package wechat

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/notify"
)

const (
	defaultAPIBase      = "https://api.weixin.qq.com"
	maxResponseBytes    = 64 << 10
	maxArticleRunes     = 20000
	maxArticleBytes     = 1 << 20
	tokenRefreshMargin  = 5 * time.Minute
	defaultRequestLimit = 10 * time.Second
)

//go:embed cover.png
var defaultCoverPNG []byte

// Client creates draft articles for one Official Account. It intentionally has
// no publish or mass-send methods: this channel can only stage content for a
// human editor.
type Client struct {
	http         *http.Client
	name         string
	appID        string
	appSecret    string
	thumbMediaID string
	author       string
	baseURL      string
	now          func() time.Time

	tokenMu    sync.Mutex
	token      string
	tokenUntil time.Time

	coverMu sync.Mutex
	coverID string
}

// NewDraftClient returns a client backed by the public WeChat API.
func NewDraftClient(name, appID, appSecret, thumbMediaID, author string, timeout time.Duration) *Client {
	return newDraftClient(name, appID, appSecret, thumbMediaID, author, timeout, defaultAPIBase)
}

func newDraftClient(name, appID, appSecret, thumbMediaID, author string, timeout time.Duration, baseURL string) *Client {
	if strings.TrimSpace(name) == "" {
		name = "wechat-draft"
	}
	if timeout < time.Second {
		timeout = defaultRequestLimit
	}
	return &Client{
		http: &http.Client{
			Timeout: timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		name:         name,
		appID:        strings.TrimSpace(appID),
		appSecret:    strings.TrimSpace(appSecret),
		thumbMediaID: strings.TrimSpace(thumbMediaID),
		author:       oneLine(author),
		baseURL:      strings.TrimRight(baseURL, "/"),
		now:          time.Now,
	}
}

func (c *Client) Name() string { return c.name }

func (c *Client) Close() {
	if c.http != nil {
		c.http.CloseIdleConnections()
	}
}

// Send creates one draft. A successful return means the draft exists in the
// WeChat draft box; it never means an article was published or mass-sent.
func (c *Client) Send(ctx context.Context, message notify.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if message.Article == nil {
		return c.deliveryError(notify.Permanent, "wechat_article_required", 0)
	}
	article := notify.Article{
		Title:  oneLine(message.Article.Title),
		Digest: oneLine(message.Article.Digest),
		Body:   strings.TrimSpace(message.Article.Body),
	}
	if err := validateArticle(article, c.author); err != nil {
		return c.deliveryError(notify.Permanent, "wechat_invalid_article: "+err.Error(), 0)
	}
	content, err := RenderHTML(article.Body)
	if err != nil {
		return c.deliveryError(notify.Permanent, "wechat_content_render", 0)
	}
	if utf8.RuneCountInString(content) >= maxArticleRunes || len(content) >= maxArticleBytes {
		return c.deliveryError(notify.Permanent, "wechat_content_too_large", 0)
	}
	thumb, err := c.coverMediaID(ctx)
	if err != nil {
		return err
	}
	_, err = c.addDraft(ctx, article, content, thumb)
	return err
}

func validateArticle(article notify.Article, author string) error {
	switch {
	case article.Title == "":
		return errors.New("title is required")
	case utf8.RuneCountInString(article.Title) > 32:
		return errors.New("title exceeds 32 characters")
	case utf8.RuneCountInString(author) > 16:
		return errors.New("author exceeds 16 characters")
	case utf8.RuneCountInString(article.Digest) > 120:
		return errors.New("digest exceeds 120 characters")
	case article.Body == "":
		return errors.New("body is required")
	default:
		return nil
	}
}

func oneLine(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

type draftRequest struct {
	Articles []draftArticle `json:"articles"`
}

type draftArticle struct {
	ArticleType        string `json:"article_type"`
	Title              string `json:"title"`
	Author             string `json:"author,omitempty"`
	Digest             string `json:"digest,omitempty"`
	Content            string `json:"content"`
	ThumbMediaID       string `json:"thumb_media_id"`
	NeedOpenComment    int    `json:"need_open_comment"`
	OnlyFansCanComment int    `json:"only_fans_can_comment"`
}

type apiResponse struct {
	ErrCode     *int   `json:"errcode"`
	ErrMsg      string `json:"errmsg"`
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
	MediaID     string `json:"media_id"`
}

type apiError struct {
	code int
	err  *notify.DeliveryError
}

func (e *apiError) Error() string { return e.err.Error() }
func (e *apiError) Unwrap() error { return e.err }

func (c *Client) addDraft(ctx context.Context, article notify.Article, content, thumb string) (string, error) {
	payload := draftRequest{Articles: []draftArticle{{
		ArticleType:        "news",
		Title:              article.Title,
		Author:             c.author,
		Digest:             article.Digest,
		Content:            content,
		ThumbMediaID:       thumb,
		NeedOpenComment:    0,
		OnlyFansCanComment: 0,
	}}}
	for attempt := 0; attempt < 2; attempt++ {
		token, err := c.accessToken(ctx)
		if err != nil {
			return "", err
		}
		endpoint := c.baseURL + "/cgi-bin/draft/add?" + tokenQuery(token)
		response, err := c.postJSON(ctx, endpoint, payload)
		if err != nil {
			if attempt == 0 && isTokenError(err) {
				c.invalidateToken()
				continue
			}
			return "", err
		}
		if response.MediaID == "" {
			return "", c.deliveryError(notify.Unknown, "wechat_missing_draft_media_id", 0)
		}
		return response.MediaID, nil
	}
	return "", c.deliveryError(notify.Retryable, "wechat_token_refresh_failed", 0)
}

func (c *Client) coverMediaID(ctx context.Context) (string, error) {
	if c.thumbMediaID != "" {
		return c.thumbMediaID, nil
	}
	c.coverMu.Lock()
	defer c.coverMu.Unlock()
	if c.coverID != "" {
		return c.coverID, nil
	}
	for attempt := 0; attempt < 2; attempt++ {
		token, err := c.accessToken(ctx)
		if err != nil {
			return "", err
		}
		mediaID, err := c.uploadCover(ctx, token)
		if err != nil {
			if attempt == 0 && isTokenError(err) {
				c.invalidateToken()
				continue
			}
			return "", err
		}
		c.coverID = mediaID
		return mediaID, nil
	}
	return "", c.deliveryError(notify.Retryable, "wechat_token_refresh_failed", 0)
}

func (c *Client) uploadCover(ctx context.Context, token string) (string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("media", "cover.png")
	if err != nil {
		return "", c.deliveryError(notify.Permanent, "wechat_cover_encode", 0)
	}
	if _, err := part.Write(defaultCoverPNG); err != nil {
		return "", c.deliveryError(notify.Permanent, "wechat_cover_encode", 0)
	}
	if err := writer.Close(); err != nil {
		return "", c.deliveryError(notify.Permanent, "wechat_cover_encode", 0)
	}
	endpoint := c.baseURL + "/cgi-bin/material/add_material?access_token=" + url.QueryEscape(token) + "&type=image"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		return "", c.deliveryError(notify.Permanent, "wechat_request_build", 0)
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response, err := c.do(request)
	if err != nil {
		return "", err
	}
	if response.MediaID == "" {
		return "", c.deliveryError(notify.Unknown, "wechat_missing_cover_media_id", 0)
	}
	return response.MediaID, nil
}

func (c *Client) accessToken(ctx context.Context) (string, error) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	if c.token != "" && c.now().Before(c.tokenUntil) {
		return c.token, nil
	}
	payload := map[string]string{
		"grant_type": "client_credential",
		"appid":      c.appID,
		"secret":     c.appSecret,
	}
	response, err := c.postJSON(ctx, c.baseURL+"/cgi-bin/stable_token", payload)
	if err != nil {
		return "", err
	}
	if response.AccessToken == "" {
		return "", c.deliveryError(notify.Unknown, "wechat_missing_access_token", 0)
	}
	ttl := time.Duration(response.ExpiresIn)*time.Second - tokenRefreshMargin
	if ttl < time.Minute {
		ttl = time.Duration(response.ExpiresIn) * time.Second / 2
	}
	if ttl < time.Minute {
		ttl = time.Minute
	}
	c.token = response.AccessToken
	c.tokenUntil = c.now().Add(ttl)
	return c.token, nil
}

func (c *Client) invalidateToken() {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	c.token = ""
	c.tokenUntil = time.Time{}
}

func (c *Client) postJSON(ctx context.Context, endpoint string, payload any) (apiResponse, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return apiResponse{}, c.deliveryError(notify.Permanent, "wechat_payload_encode", 0)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return apiResponse{}, c.deliveryError(notify.Permanent, "wechat_request_build", 0)
	}
	request.Header.Set("Content-Type", "application/json")
	return c.do(request)
}

func (c *Client) do(request *http.Request) (apiResponse, error) {
	response, err := c.http.Do(request)
	if err != nil {
		if request.Context().Err() != nil {
			return apiResponse{}, request.Context().Err()
		}
		return apiResponse{}, c.deliveryError(notify.Retryable, "wechat_network", 0)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes {
		return apiResponse{}, c.deliveryError(notify.Unknown, "wechat_response_read", response.StatusCode)
	}
	if response.StatusCode != http.StatusOK {
		return apiResponse{}, c.httpError(response.StatusCode, response.Header.Get("Retry-After"))
	}
	var result apiResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return apiResponse{}, c.deliveryError(notify.Unknown, "wechat_response_decode", response.StatusCode)
	}
	if result.ErrCode != nil && *result.ErrCode != 0 {
		return apiResponse{}, c.apiFailure(*result.ErrCode, response.StatusCode)
	}
	return result, nil
}

func (c *Client) httpError(status int, retryAfter string) error {
	kind := notify.Permanent
	if status == http.StatusTooManyRequests || status >= http.StatusInternalServerError {
		kind = notify.Retryable
	}
	failure := c.deliveryError(kind, "wechat_http_status", status)
	if delay := parseRetryAfter(retryAfter, c.now()); delay > 0 {
		failure.RetryAfter = delay
	}
	return failure
}

func (c *Client) apiFailure(code, status int) error {
	kind := notify.Permanent
	delay := time.Duration(0)
	switch code {
	case -1, 40001, 42001, 45009, 45011, 45066, 89504, 89505, 268488001:
		kind = notify.Retryable
	}
	switch code {
	case 45011, 45066:
		delay = time.Minute
	case 89504, 89505:
		delay = 5 * time.Minute
	}
	reason := fmt.Sprintf("wechat_api_%d", code)
	if label := apiReason(code); label != "" {
		reason += "_" + label
	}
	failure := c.deliveryError(kind, reason, status)
	failure.RetryAfter = delay
	return &apiError{code: code, err: failure}
}

func apiReason(code int) string {
	return map[int]string{
		40001: "invalid_credential",
		40007: "invalid_media_id",
		40164: "ip_not_whitelisted",
		45009: "api_daily_quota",
		45011: "api_rate_limited",
		48001: "api_unauthorized",
		48004: "api_forbidden",
		48023: "account_banned",
		50002: "user_limited",
		53504: "publish_quiz_required",
	}[code]
}

func isTokenError(err error) bool {
	var failure *apiError
	return errors.As(err, &failure) && (failure.code == 40001 || failure.code == 42001)
}

func (c *Client) deliveryError(kind notify.Kind, reason string, status int) *notify.DeliveryError {
	return &notify.DeliveryError{Channel: c.name, Kind: kind, Status: status, Reason: reason}
}

func tokenQuery(token string) string {
	return "access_token=" + url.QueryEscape(token)
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil && when.After(now) {
		return when.Sub(now)
	}
	return 0
}
