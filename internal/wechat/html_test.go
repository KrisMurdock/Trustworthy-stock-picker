package wechat

import (
	"strings"
	"testing"
)

func TestRenderHTMLBuildsStructuredWechatArticle(t *testing.T) {
	body := "# 10月22日 观察 & 风险\n\n" +
		"## 一、排名靠前的观察对象\n\n" +
		"### 1. 候选一（sz000001）\n\n" +
		"- 结构状态：通过\n" +
		"- 推荐：15.00~15.30 购入 <script>alert(1)</script>\n" +
		"- 日价格区间：14.80~15.50\n\n" +
		"正文 A & B。\n\n" +
		"> 强势观察池，而不是正式推荐池。\n\n" +
		"1. 日线结构能否保持；\n" +
		"2. 30分钟是否出现失效信号。\n"
	rendered, err := RenderHTML(body)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"<h1>10月22日 观察 &amp; 风险</h1>",
		"<h2>一、排名靠前的观察对象</h2>",
		"<h3>1. 候选一（sz000001）</h3>",
		"<ul><li>结构状态：通过</li><li>推荐：15.00~15.30 购入 &lt;script&gt;alert(1)&lt;/script&gt;</li><li>日价格区间：14.80~15.50</li></ul>",
		"<p>正文 A &amp; B。</p>",
		"<blockquote><p>强势观察池，而不是正式推荐池。</p></blockquote>",
		"<ol><li>日线结构能否保持；</li><li>30分钟是否出现失效信号。</li></ol>",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("missing %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "<img") || strings.Contains(rendered, "<script>") || strings.Contains(rendered, "<a ") {
		t.Fatalf("unsafe HTML emitted: %s", rendered)
	}
}

func TestRenderHTMLKeepsLegacyHeadingAndIndentedLines(t *testing.T) {
	rendered, err := RenderHTML("先说结论\n正文 A & B。\n\n一、精排名单\n   缩进说明 <script>alert(1)</script>\n风险提示\n仅供研究。\n")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"<h2>先说结论</h2>",
		"<p>正文 A &amp; B。</p>",
		"<h2>一、精排名单</h2>",
		"<p>&nbsp;&nbsp;&nbsp;缩进说明 &lt;script&gt;alert(1)&lt;/script&gt;</p>",
		"<h2>风险提示</h2>",
		"<p>仅供研究。</p>",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("missing %q:\n%s", want, rendered)
		}
	}
}

func TestRenderHTMLRejectsEmptyBody(t *testing.T) {
	if _, err := RenderHTML(" \n\t\n"); err == nil {
		t.Fatal("accepted empty body")
	}
}
