package report

import (
	"strings"
	"testing"
	"unicode"
)

func TestArticleStockLabelUsesUppercaseInitials(t *testing.T) {
	tests := map[string]string{
		"温氏股份":   "WSGF",
		"长城军工":   "CCJG",
		"重庆啤酒":   "CQPJ",
		"招商银行":   "ZSYH",
		"厦门钨业":   "XMWY",
		"中国重工":   "ZGZG",
		"TCL科技":  "TCLKJ",
		"中芯国际-U": "ZXGJU",
	}
	for name, want := range tests {
		got := articleStockLabel("sz000001", name)
		if got != want {
			t.Fatalf("articleStockLabel(%q) = %q, want %q", name, got, want)
		}
		if strings.ContainsFunc(got, func(r rune) bool { return unicode.Is(unicode.Han, r) }) {
			t.Fatalf("label %q contains Han characters", got)
		}
	}
	if got := articleStockLabel("sz000001", ""); got != "sz000001" {
		t.Fatalf("empty name fallback = %q", got)
	}
}
