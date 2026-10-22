package wechat

import (
	"strings"
	"testing"
)

func TestRenderHTMLBuildsWechatSafeParagraphs(t *testing.T) {
	html, err := RenderHTML("先说结论\n正文 A & B。\n\n一、精排名单\n1. 候选一\n   缩进说明 <script>alert(1)</script>\n风险提示\n仅供研究。\n")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"<p><strong>先说结论</strong></p>",
		"<p>正文 A &amp; B。</p>",
		"<p><strong>一、精排名单</strong></p>",
		"<p><strong>1. 候选一</strong></p>",
		"<p>&nbsp;&nbsp;&nbsp;缩进说明 &lt;script&gt;alert(1)&lt;/script&gt;</p>",
		"<p><strong>风险提示</strong></p>",
		"<p>仅供研究。</p>",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("missing %q:\n%s", want, html)
		}
	}
	if strings.Contains(html, "<img") || strings.Contains(html, "<script>") {
		t.Fatalf("unsafe HTML emitted: %s", html)
	}
}

func TestRenderHTMLRejectsEmptyBody(t *testing.T) {
	if _, err := RenderHTML(" \n\t\n"); err == nil {
		t.Fatal("accepted empty body")
	}
}
