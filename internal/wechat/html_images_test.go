package wechat

import (
	"strings"
	"testing"
)

func TestRenderHTMLWithImagesInsertsUploadedChart(t *testing.T) {
	rendered, err := RenderHTMLWithImages(
		":::card blue\n### 1. YLWL（sz300628）\n\n#### 结构图谱\n[[chart:chart-sz300628]]\n:::\n",
		map[string]RenderedImage{"chart-sz300628": {URL: "https://mmbiz.qpic.cn/chart?x=1&y=2", Caption: "sz300628 日 K 结构图 <研究>"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`<img src="https://mmbiz.qpic.cn/chart?x=1&amp;y=2"`,
		`alt="sz300628 日 K 结构图 &lt;研究&gt;"`,
		"sz300628 日 K 结构图 &lt;研究&gt;",
		"display:block;width:100%;height:auto;",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("missing %q:\n%s", want, rendered)
		}
	}
}

func TestRenderHTMLWithImagesFailsClosed(t *testing.T) {
	if _, err := RenderHTML("[[chart:chart-missing]]"); err == nil {
		t.Fatal("missing image was accepted")
	}
	if _, err := RenderHTMLWithImages("[[chart:chart-a]]", map[string]RenderedImage{"chart-a": {URL: "javascript:alert(1)"}}); err == nil {
		t.Fatal("unsafe image URL was accepted")
	}
	if _, err := RenderHTMLWithImages("[[chart:chart-a]]", map[string]RenderedImage{"chart-a": {URL: "https://user:pass@example.com/a.png"}}); err == nil {
		t.Fatal("credential-bearing image URL was accepted")
	}
}
