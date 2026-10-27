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
		`>10月22日 观察 &amp; 风险</h1>`,
		`>一、排名靠前的观察对象</h2>`,
		`>1. 候选一（sz000001）</h3>`,
		`<li>推荐：15.00~15.30 购入 &lt;script&gt;alert(1)&lt;/script&gt;</li>`,
		`>正文 A &amp; B。</p>`,
		`>强势观察池，而不是正式推荐池。</p></blockquote>`,
		`<li>30分钟是否出现失效信号。</li>`,
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
		`>先说结论</h2>`,
		`>正文 A &amp; B。</p>`,
		`>一、精排名单</h2>`,
		`>&nbsp;&nbsp;&nbsp;缩进说明 &lt;script&gt;alert(1)&lt;/script&gt;</p>`,
		`>风险提示</h2>`,
		`>仅供研究。</p>`,
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

func TestRenderHTMLBuildsSafeStockCard(t *testing.T) {
	body := "# 每日精选\n\n" +
		":::card blue\n" +
		"### 1. 候选一（sz000001）\n" +
		"#### 技术指标\n" +
		"**结论**：等待确认。\n" +
		"- **RSI14**：58.20\n" +
		"- 收盘位置：<script>alert(1)</script>\n\n" +
		"> 仅为观察，不构成投资建议。\n\n" +
		"1. **触发规则**：放量站稳 MA20。\n" +
		":::"
	rendered, err := RenderHTML(body)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`<section style="` + cardShellOpenStyle + `#2563eb` + cardShellStyleSuffix + `">`,
		`<h3 style="` + cardHeading3Style + `">1. 候选一（sz000001）</h3>`,
		`<h4 style="` + cardHeading4Prefix + `#2563eb` + cardHeading4Suffix + `">技术指标</h4>`,
		`<strong>结论</strong>：等待确认。</p>`,
		`<li><strong>RSI14</strong>：58.20</li>`,
		`<li>收盘位置：&lt;script&gt;alert(1)&lt;/script&gt;</li>`,
		`<blockquote style="` + cardQuoteStylePrefix + `#2563eb` + cardQuoteStyleSuffix + `"><p style="` + cardQuoteTextStyle + `">仅为观察，不构成投资建议。</p></blockquote>`,
		`<li><strong>触发规则</strong>：放量站稳 MA20。</li>`,
		`</section>`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("missing %q:\n%s", want, rendered)
		}
	}
	for _, forbidden := range []string{"<script", "<style", "<a ", "<img", " class=", " onerror="} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("unsafe HTML %q emitted: %s", forbidden, rendered)
		}
	}
}

func TestRenderHTMLUsesFixedToneColors(t *testing.T) {
	tests := []struct {
		tone  string
		color string
	}{
		{tone: "blue", color: "#2563eb"},
		{tone: "green", color: "#16a34a"},
		{tone: "amber", color: "#d97706"},
		{tone: "grey", color: "#64748b"},
	}
	for _, test := range tests {
		t.Run(test.tone, func(t *testing.T) {
			rendered, err := RenderHTML(":::card " + test.tone + "\n#### 指标\n**RSI**：50\n:::")
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{
				"border-left:4px solid " + test.color + ";",
				"color:" + test.color + ";",
			} {
				if !strings.Contains(rendered, want) {
					t.Fatalf("missing fixed tone style %q:\n%s", want, rendered)
				}
			}
		})
	}
}

func TestRenderHTMLUnknownToneFallsBackToNeutral(t *testing.T) {
	rendered, err := RenderHTML(":::card orange\n#### 指标\n内容\n:::")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered, "border-left:4px solid #2563eb;") {
		t.Fatalf("unknown tone did not use neutral color:\n%s", rendered)
	}
}

func TestRenderHTMLSupportsH4AndBoldOutsideCards(t *testing.T) {
	rendered, err := RenderHTML("#### 指标概览\n**RSI14** 为 58.20\n**未闭合标记保持原样\n- **MA20**：15.00")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`<h4 style="` + articleHeading4Style + `">指标概览</h4>`,
		"><strong>RSI14</strong> 为 58.20</p>",
		"<p>**未闭合标记保持原样</p>",
		"<li><strong>MA20</strong>：15.00</li>",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("missing %q:\n%s", want, rendered)
		}
	}
}

func TestRenderHTMLRejectsMalformedCards(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "unclosed", body: ":::card\n### 标题\n内容"},
		{name: "nested", body: ":::card\n### 外层\n:::card green\n### 内层\n:::\n:::"},
		{name: "stray closing fence", body: "### 标题\n:::"},
		{name: "empty", body: ":::card\n\n:::"},
		{name: "extra closing fence", body: ":::card\n内容\n:::\n:::"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := RenderHTML(test.body); err == nil {
				t.Fatalf("accepted malformed card: %s", test.body)
			}
		})
	}
}
