package wechat

import (
	"errors"
	"html"
	"strings"
)

// RenderHTML converts the deterministic plain-text article body into the small
// HTML subset accepted by WeChat drafts. It deliberately emits no images or
// external links, so the draft cannot accidentally depend on blocked assets.
func RenderHTML(body string) (string, error) {
	body = strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\r", "\n")
	var out strings.Builder
	for _, raw := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		indent := len(raw) - len(strings.TrimLeft(raw, " \t"))
		escaped := html.EscapeString(trimmed)
		if indent > 0 {
			if indent > 8 {
				indent = 8
			}
			escaped = strings.Repeat("&nbsp;", indent) + escaped
		}
		if isHeading(trimmed) || isNumberedLine(trimmed) {
			out.WriteString("<p><strong>")
			out.WriteString(escaped)
			out.WriteString("</strong></p>")
			continue
		}
		out.WriteString("<p>")
		out.WriteString(escaped)
		out.WriteString("</p>")
	}
	if out.Len() == 0 {
		return "", errors.New("empty article body")
	}
	return out.String(), nil
}

func isHeading(line string) bool {
	if line == "先说结论" || line == "风险提示" {
		return true
	}
	runes := []rune(line)
	return len(runes) >= 3 && strings.ContainsRune("一二三四五六七八九十", runes[0]) && runes[1] == '、'
}

func isNumberedLine(line string) bool {
	index := strings.Index(line, ". ")
	if index < 1 || index > 3 {
		return false
	}
	for _, r := range line[:index] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
