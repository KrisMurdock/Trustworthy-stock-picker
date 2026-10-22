package report

import (
	"strings"
	"unicode"

	"github.com/mozillazg/go-pinyin"
)

// articleStockLabel masks the stock name in the public article. Chinese names
// are reduced to uppercase pinyin initials; Latin letters and digits are kept
// so labels such as TCL科技 become TCLKJ. An empty conversion falls back to the
// stock code rather than exposing the original name.
func articleStockLabel(code, name string) string {
	label := stockNameInitials(name)
	if label == "" {
		return strings.TrimSpace(code)
	}
	return label
}

func stockNameInitials(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	args := pinyin.NewArgs()
	args.Style = pinyin.FirstLetter
	runes := []rune(name)
	var out strings.Builder
	for index, value := range runes {
		if value <= unicode.MaxASCII {
			switch {
			case value >= 'a' && value <= 'z':
				out.WriteRune(value - 'a' + 'A')
			case value >= 'A' && value <= 'Z', value >= '0' && value <= '9':
				out.WriteRune(value)
			}
			continue
		}
		if !unicode.Is(unicode.Han, value) {
			continue
		}
		switch value {
		case '长':
			out.WriteByte('C')
		case '厦':
			out.WriteByte('X')
		case '乐':
			out.WriteByte('L')
		case '莞':
			out.WriteByte('G')
		case '行':
			out.WriteByte('H')
		case '重':
			if index+1 < len(runes) && runes[index+1] == '庆' {
				out.WriteByte('C')
			} else {
				out.WriteByte('Z')
			}
		default:
			letters := pinyin.SinglePinyin(value, args)
			if len(letters) == 0 || letters[0] == "" {
				continue
			}
			initial := strings.ToUpper(string([]rune(letters[0])[0]))
			if isASCIIAlpha(initial) {
				out.WriteString(initial)
			}
		}
	}
	return out.String()
}

func isASCIIAlpha(value string) bool {
	if len(value) != 1 {
		return false
	}
	char := value[0]
	return char >= 'A' && char <= 'Z'
}
