package config

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type wechatCredentials struct {
	AppID        string
	AppSecret    string
	ThumbMediaID string
	Author       string
}

func loadWechatCredentials(baseDir, path string) (wechatCredentials, error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(baseDir, path)
	}
	f, err := os.Open(path)
	if err != nil {
		return wechatCredentials{}, errors.New("cannot open WeChat credentials file")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 64<<10+1))
	if err != nil || len(data) > 64<<10 {
		return wechatCredentials{}, errors.New("invalid WeChat credentials file")
	}
	credentials, err := parseWechatCredentials(string(data))
	if err != nil {
		return wechatCredentials{}, errors.New("invalid WeChat credentials file: expected AppID and AppSecret")
	}
	return credentials, nil
}

func parseWechatCredentials(text string) (wechatCredentials, error) {
	text = strings.TrimPrefix(strings.ReplaceAll(text, "\r\n", "\n"), "\ufeff")
	lines := make([]string, 0, 8)
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return wechatCredentials{}, errors.New("empty file")
	}

	credentials := wechatCredentials{}
	if assignmentMode(lines) {
		if len(lines) > 0 && !hasAssignment(lines[0]) {
			lines = lines[1:]
			if len(lines) > 0 && !hasAssignment(lines[0]) {
				lines = lines[1:]
			}
		}
		for _, line := range lines {
			key, value, ok := splitAssignment(line)
			if !ok {
				return wechatCredentials{}, errors.New("malformed credential line")
			}
			if err := credentials.set(key, value); err != nil {
				return wechatCredentials{}, err
			}
		}
	} else {
		if strings.Contains(lines[0], "公众号") {
			lines = lines[1:]
			if len(lines) > 0 && !isWechatCredentialLabel(lines[0]) {
				lines = lines[1:]
			}
		}
		for index := 0; index < len(lines); index += 2 {
			key := lines[index]
			if !isWechatCredentialLabel(key) || index+1 >= len(lines) || isWechatCredentialLabel(lines[index+1]) {
				return wechatCredentials{}, errors.New("malformed credential label")
			}
			if err := credentials.set(key, lines[index+1]); err != nil {
				return wechatCredentials{}, err
			}
		}
	}
	if strings.TrimSpace(credentials.AppID) == "" || strings.TrimSpace(credentials.AppSecret) == "" {
		return wechatCredentials{}, errors.New("missing AppID or AppSecret")
	}
	return credentials, nil
}

func assignmentMode(lines []string) bool {
	index := 0
	if strings.Contains(lines[0], "公众号") {
		index++
		if index < len(lines) && !hasAssignment(lines[index]) {
			index++
		}
	}
	if index >= len(lines) {
		return false
	}
	for _, line := range lines[index:] {
		if !hasAssignment(line) {
			return false
		}
	}
	return true
}

func hasAssignment(line string) bool {
	_, _, ok := splitAssignment(line)
	return ok
}

func splitAssignment(line string) (string, string, bool) {
	index := strings.IndexAny(line, ":=")
	if index <= 0 {
		return "", "", false
	}
	key := strings.TrimSpace(line[:index])
	value := strings.TrimSpace(line[index+1:])
	value = strings.Trim(value, `"'`)
	if key == "" || value == "" {
		return "", "", false
	}
	return key, value, true
}

func isWechatCredentialLabel(value string) bool {
	switch strings.ToLower(strings.NewReplacer(" ", "", "_", "", "-", "").Replace(strings.TrimSpace(value))) {
	case "appid", "appsecret", "thumbmediaid", "author":
		return true
	default:
		return false
	}
}

func (credentials *wechatCredentials) set(key, value string) error {
	value = strings.Trim(strings.TrimSpace(value), `"'`)
	if value == "" {
		return errors.New("empty credential value")
	}
	switch strings.ToLower(strings.NewReplacer(" ", "", "_", "", "-", "").Replace(strings.TrimSpace(key))) {
	case "appid":
		if credentials.AppID != "" {
			return errors.New("duplicate AppID")
		}
		credentials.AppID = value
	case "appsecret":
		if credentials.AppSecret != "" {
			return errors.New("duplicate AppSecret")
		}
		credentials.AppSecret = value
	case "thumbmediaid":
		if credentials.ThumbMediaID != "" {
			return errors.New("duplicate ThumbMediaID")
		}
		credentials.ThumbMediaID = value
	case "author":
		if credentials.Author != "" {
			return errors.New("duplicate Author")
		}
		credentials.Author = value
	default:
		return errors.New("unknown credential key")
	}
	return nil
}
