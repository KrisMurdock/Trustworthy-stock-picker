package wechat

import (
	"errors"
	"html"
	"net/url"
	"regexp"
	"strings"
)

const (
	cardShellOpenStyle   = "margin:18px 0;border:1px solid #dbe5f0;border-radius:12px;background:#fff;overflow:hidden;"
	cardBarStylePrefix   = "height:4px;background:"
	cardBarStyleSuffix   = ";font-size:0;line-height:0;"
	cardContentOpenStyle = "padding:16px;color:#334155;font-size:15px;line-height:1.7;"
	cardHeading3Style    = "margin:0 0 10px;color:#0f172a;font-size:19px;line-height:1.4;font-weight:700;"
	cardHeading4Prefix   = "margin:14px 0 6px;color:"
	cardHeading4Suffix   = ";font-size:15px;"
	cardQuoteStylePrefix = "margin:10px 0;padding:8px 12px;border-left:3px solid "
	cardQuoteStyleSuffix = ";background:#f8fafc;"
	cardQuoteTextStyle   = "margin:0;font-size:15px;"
	articleHeading4Style = "margin:18px 0 8px;color:#1e3a8a;font-size:16px;line-height:1.5;font-weight:700;"
	cardBlueColor        = "#2563eb"
	cardGreenColor       = "#16a34a"
	cardAmberColor       = "#d97706"
	cardGreyColor        = "#64748b"
	cardNeutralColor     = "#2563eb"
)

type cardTheme struct {
	barColor string
	accent   string
}

var cardThemes = map[string]cardTheme{
	"neutral": {barColor: cardNeutralColor, accent: cardNeutralColor},
	"blue":    {barColor: cardBlueColor, accent: cardBlueColor},
	"green":   {barColor: cardGreenColor, accent: cardGreenColor},
	"amber":   {barColor: cardAmberColor, accent: cardAmberColor},
	"grey":    {barColor: cardGreyColor, accent: cardGreyColor},
	"gray":    {barColor: cardGreyColor, accent: cardGreyColor},
}

// RenderedImage is one uploaded chart that can be referenced from the body.
type RenderedImage struct {
	URL     string
	Caption string
}

var imageTokenPattern = regexp.MustCompile(`^\[\[chart:([a-z0-9][a-z0-9-]{0,63})\]\]$`)

// RenderHTML converts the deterministic article body into the small HTML
// subset accepted by WeChat drafts. Bodies without chart tokens still emit no
// images, links, classes, external styles or inline scripts.
func RenderHTML(body string) (string, error) {
	return RenderHTMLWithImages(body, nil)
}

// RenderHTMLWithImages resolves chart placeholders against images uploaded to
// the WeChat CDN. A referenced but missing image fails closed.
func RenderHTMLWithImages(body string, images map[string]RenderedImage) (string, error) {
	body = strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\r", "\n")
	var out strings.Builder
	if err := renderLines(&out, strings.Split(body, "\n"), nil, images); err != nil {
		return "", err
	}
	if out.Len() == 0 {
		return "", errors.New("empty article body")
	}
	return out.String(), nil
}

func renderLines(out *strings.Builder, lines []string, card *cardTheme, images map[string]RenderedImage) error {
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

	for index := 0; index < len(lines); index++ {
		raw := lines[index]
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			flushList()
			continue
		}

		if tone, ok := parseCardOpen(trimmed); ok {
			if card != nil {
				return errors.New("nested card blocks are not supported")
			}
			flushList()
			end, err := findCardEnd(lines, index+1)
			if err != nil {
				return err
			}
			if cardLinesEmpty(lines[index+1 : end]) {
				return errors.New("empty card block")
			}

			theme := cardThemeFor(tone)
			var inner strings.Builder
			if err := renderLines(&inner, lines[index+1:end], &theme, images); err != nil {
				return err
			}
			if inner.Len() == 0 {
				return errors.New("empty card block")
			}

			out.WriteString(`<section style="`)
			out.WriteString(cardShellOpenStyle)
			out.WriteString(`">`)
			out.WriteString(`<section style="`)
			out.WriteString(cardBarStylePrefix)
			out.WriteString(theme.barColor)
			out.WriteString(cardBarStyleSuffix)
			out.WriteString(`"></section>`)
			out.WriteString(`<section style="`)
			out.WriteString(cardContentOpenStyle)
			out.WriteString(`">`)
			out.WriteString(inner.String())
			out.WriteString(`</section></section>`)
			index = end
			continue
		}

		if trimmed == ":::" {
			flushList()
			return errors.New("unexpected card closing fence")
		}

		if imageID, ok := parseImageToken(trimmed); ok {
			flushList()
			image, exists := images[imageID]
			if !exists {
				return errors.New("article chart is unavailable: " + imageID)
			}
			if err := writeImage(out, image, card); err != nil {
				return err
			}
			continue
		}

		switch {
		case strings.HasPrefix(trimmed, "#### "):
			flushList()
			if card == nil {
				writeHeading(out, "h4", strings.TrimSpace(trimmed[5:]), articleHeading4Style)
			} else {
				writeHeading(out, "h4", strings.TrimSpace(trimmed[5:]), cardHeading4Prefix+card.accent+cardHeading4Suffix)
			}
		case strings.HasPrefix(trimmed, "### "):
			flushList()
			if card == nil {
				writeHeading(out, "h3", strings.TrimSpace(trimmed[4:]), "")
			} else {
				writeHeading(out, "h3", strings.TrimSpace(trimmed[4:]), cardHeading3Style)
			}
		case strings.HasPrefix(trimmed, "## "):
			flushList()
			if card != nil {
				return errors.New("card blocks only allow level 3 and level 4 headings")
			}
			writeHeading(out, "h2", strings.TrimSpace(trimmed[3:]), "")
		case strings.HasPrefix(trimmed, "# "):
			flushList()
			if card != nil {
				return errors.New("card blocks only allow level 3 and level 4 headings")
			}
			writeHeading(out, "h1", strings.TrimSpace(trimmed[2:]), "")
		case strings.HasPrefix(trimmed, "- "):
			if list != "ul" {
				flushList()
				writeListOpen(out, "ul")
				list = "ul"
			}
			writeListItem(out, strings.TrimSpace(trimmed[2:]))
		case isNumberedLine(trimmed):
			if list != "ol" {
				flushList()
				writeListOpen(out, "ol")
				list = "ol"
			}
			itemIndex := strings.Index(trimmed, ". ")
			writeListItem(out, strings.TrimSpace(trimmed[itemIndex+2:]))
		case strings.HasPrefix(trimmed, "> "):
			flushList()
			if card == nil {
				out.WriteString("<blockquote><p>")
				writeInline(out, strings.TrimSpace(trimmed[2:]))
				out.WriteString("</p></blockquote>")
			} else {
				out.WriteString(`<blockquote style="`)
				out.WriteString(cardQuoteStylePrefix)
				out.WriteString(card.accent)
				out.WriteString(cardQuoteStyleSuffix)
				out.WriteString(`"><p style="`)
				out.WriteString(cardQuoteTextStyle)
				out.WriteString(`">`)
				writeInline(out, strings.TrimSpace(trimmed[2:]))
				out.WriteString("</p></blockquote>")
			}
		case isHeading(trimmed):
			flushList()
			if card != nil {
				return errors.New("card blocks only allow level 3 and level 4 headings")
			}
			writeHeading(out, "h2", trimmed, "")
		default:
			flushList()
			indent := len(raw) - len(strings.TrimLeft(raw, " \t"))
			if indent > 8 {
				indent = 8
			}
			out.WriteString("<p>")
			out.WriteString(strings.Repeat("&nbsp;", indent))
			writeInline(out, trimmed)
			out.WriteString("</p>")
		}
	}
	flushList()
	return nil
}

func parseImageToken(line string) (string, bool) {
	match := imageTokenPattern.FindStringSubmatch(line)
	if len(match) != 2 {
		return "", false
	}
	return match[1], true
}

func writeImage(out *strings.Builder, image RenderedImage, card *cardTheme) error {
	parsed, err := url.Parse(image.URL)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.User != nil {
		return errors.New("article chart URL is invalid")
	}
	caption := strings.TrimSpace(image.Caption)
	out.WriteString(`<section style="margin:14px 0;">`)
	out.WriteString(`<img src="`)
	out.WriteString(html.EscapeString(image.URL))
	out.WriteString(`" alt="`)
	out.WriteString(html.EscapeString(caption))
	out.WriteString(`" style="display:block;width:100%;height:auto;border:1px solid #dbe5f0;border-radius:8px;" />`)
	if caption != "" {
		out.WriteString(`<p style="margin:7px 0 0;color:`)
		if card != nil {
			out.WriteString(card.accent)
		} else {
			out.WriteString(cardGreyColor)
		}
		out.WriteString(`;font-size:12px;line-height:1.6;">`)
		out.WriteString(html.EscapeString(caption))
		out.WriteString(`</p>`)
	}
	out.WriteString(`</section>`)
	return nil
}

func parseCardOpen(line string) (string, bool) {
	if line == ":::card" {
		return "neutral", true
	}
	if !strings.HasPrefix(line, ":::card") {
		return "", false
	}
	rest := line[len(":::card"):]
	if rest == "" || (rest[0] != ' ' && rest[0] != '\t') {
		return "", false
	}
	fields := strings.Fields(rest)
	if len(fields) != 1 {
		return "unknown", true
	}
	return strings.ToLower(fields[0]), true
}

func findCardEnd(lines []string, start int) (int, error) {
	for index := start; index < len(lines); index++ {
		trimmed := strings.TrimSpace(lines[index])
		if _, ok := parseCardOpen(trimmed); ok {
			return 0, errors.New("nested card blocks are not supported")
		}
		if trimmed == ":::" {
			return index, nil
		}
	}
	return 0, errors.New("unclosed card block")
}

func cardLinesEmpty(lines []string) bool {
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			return false
		}
	}
	return true
}

func cardThemeFor(tone string) cardTheme {
	if theme, ok := cardThemes[strings.ToLower(strings.TrimSpace(tone))]; ok {
		return theme
	}
	return cardThemes["neutral"]
}

func writeHeading(out *strings.Builder, tag, text, style string) {
	out.WriteString("<")
	out.WriteString(tag)
	if style != "" {
		out.WriteString(` style="`)
		out.WriteString(style)
		out.WriteString(`"`)
	}
	out.WriteString(">")
	writeInline(out, text)
	out.WriteString("</")
	out.WriteString(tag)
	out.WriteString(">")
}

func writeListOpen(out *strings.Builder, tag string) {
	out.WriteString("<")
	out.WriteString(tag)
	out.WriteString(">")
}

func writeListItem(out *strings.Builder, text string) {
	out.WriteString("<li>")
	writeInline(out, text)
	out.WriteString("</li>")
}

// writeInline supports only paired **bold** markers. Markers are split after
// escaping, so the generated <strong> tags are the only raw HTML in the text
// path and user-provided tags can never become markup.
func writeInline(out *strings.Builder, text string) {
	escaped := html.EscapeString(text)
	if strings.Count(escaped, "**")%2 != 0 {
		out.WriteString(escaped)
		return
	}
	for index, part := range strings.Split(escaped, "**") {
		if index%2 == 0 {
			out.WriteString(part)
			continue
		}
		out.WriteString("<strong>")
		out.WriteString(part)
		out.WriteString("</strong>")
	}
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
