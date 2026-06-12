package mail

import (
	"strings"
	"testing"
)

func TestRenderLayoutEscapesAndPlacesContent(t *testing.T) {
	out := RenderLayout(LayoutOptions{
		Preheader:  `sneak <script>alert(1)</script>`,
		Title:      `Title & <b>bold</b>`,
		BodyHTML:   `<p id="body-marker">trusted</p>`,
		FooterHTML: `<span id="footer-marker">fine print</span>`,
	})
	if strings.Contains(out, "<script>") || strings.Contains(out, "<b>bold</b>") {
		t.Fatalf("preheader/title not escaped:\n%s", out)
	}
	if !strings.Contains(out, "Title &amp; &lt;b&gt;bold&lt;/b&gt;") {
		t.Fatalf("escaped title missing:\n%s", out)
	}
	if !strings.Contains(out, `<p id="body-marker">trusted</p>`) {
		t.Fatalf("body HTML not passed through:\n%s", out)
	}
	if !strings.Contains(out, `<span id="footer-marker">fine print</span>`) {
		t.Fatalf("footer HTML not passed through:\n%s", out)
	}
	if !strings.Contains(out, "SILO") {
		t.Fatalf("wordmark missing:\n%s", out)
	}
}

// Some emails must render fully link-free when no external URL is configured;
// the shell itself must therefore never contribute one.
func TestRenderLayoutAddsNoLinks(t *testing.T) {
	out := RenderLayout(LayoutOptions{Title: "Hello", BodyHTML: "<p>hi</p>"})
	if strings.Contains(out, "href=") {
		t.Fatalf("layout shell added a link:\n%s", out)
	}
	if strings.Contains(out, "<h1") && strings.Contains(RenderLayout(LayoutOptions{BodyHTML: "x"}), "<h1") {
		t.Fatalf("empty title should not render an <h1>")
	}
}

func TestEmailButtonEscapes(t *testing.T) {
	out := EmailButton(`Click "here" <now>`, `https://example.com/?a=1&b=<2>`)
	if !strings.Contains(out, `href="https://example.com/?a=1&amp;b=&lt;2&gt;"`) {
		t.Fatalf("href not escaped: %s", out)
	}
	if strings.Contains(out, "<now>") {
		t.Fatalf("label not escaped: %s", out)
	}
}
