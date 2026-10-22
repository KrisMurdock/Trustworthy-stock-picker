package wechat

import (
	"errors"
	"html"
	"strings"
)

// RenderHTML converts the deterministic article body into the small HTML
// subset accepted by WeChat drafts. It deliberately emits no images, links or
// inline scripts, so the draft cannot depend on blocked or untrusted assets.
func RenderHTML(body string) (string, error) {
	body = strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\r", "\n")
	var out strings.Builder
	list := ""
	flushList := func() {
		switch list {
		case "ul":
			out.WriteString("</ul>")
		case "ol":
			out.WriteString("</ol>")
		}
		list = ""
	}
	for _, raw := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			flushList()
			continue
		}
		switch {
		case strings.HasPrefix(trimmed, "### "):
			flushList()
			writeHeading(&out, "h3", strings.TrimSpace(trimmed[4:]))
		case strings.HasPrefix(trimmed, "## "):
			flushList()
			writeHeading(&out, "h2", strings.TrimSpace(trimmed[3:]))
		case strings.HasPrefix(trimmed, "# "):
			flushList()
			writeHeading(&out, "h1", strings.TrimSpace(trimmed[2:]))
		case strings.HasPrefix(trimmed, "- "):
			if list != "ul" {
				flushList()
				out.WriteString("<ul>")
				list = "ul"
			}
			out.WriteString("<li>")
			out.WriteString(html.EscapeString(strings.TrimSpace(trimmed[2:])))
			out.WriteString("</li>")
		case isNumberedLine(trimmed):
			if list != "ol" {
				flushList()
				out.WriteString("<ol>")
				list = "ol"
			}
			index := strings.Index(trimmed, ". ")
			out.WriteString("<li>")
			out.WriteString(html.EscapeString(strings.TrimSpace(trimmed[index+2:])))
			out.WriteString("</li>")
		case strings.HasPrefix(trimmed, "> "):
			flushList()
			out.WriteString("<blockquote><p>")
			out.WriteString(html.EscapeString(strings.TrimSpace(trimmed[2:])))
			out.WriteString("</p></blockquote>")
		case isHeading(trimmed):
			flushList()
			writeHeading(&out, "h2", trimmed)
		default:
			flushList()
			indent := len(raw) - len(strings.TrimLeft(raw, " \t"))
			escaped := html.EscapeString(trimmed)
			if indent > 0 {
				if indent > 8 {
					indent = 8
				}
				escaped = strings.Repeat("&nbsp;", indent) + escaped
			}
			out.WriteString("<p>")
			out.WriteString(escaped)
			out.WriteString("</p>")
		}
	}
	flushList()
	if out.Len() == 0 {
		return "", errors.New("empty article body")
	}
	return out.String(), nil
}

func writeHeading(out *strings.Builder, tag, text string) {
	out.WriteString("<")
	out.WriteString(tag)
	out.WriteString(">")
	out.WriteString(html.EscapeString(text))
	out.WriteString("</")
	out.WriteString(tag)
	out.WriteString(">")
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
