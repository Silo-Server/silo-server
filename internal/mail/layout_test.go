package mail

import (
	"strings"
	"testing"
	"time"
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
	if !strings.Contains(out, `src="cid:silo-logo"`) || !strings.Contains(out, `alt="Silo"`) {
		t.Fatalf("default logo missing:\n%s", out)
	}
}

func TestEmailButtonEscapes(t *testing.T) {
	out := EmailButton(Brand{}, `Click "here" <now>`, `https://example.com/?a=1&b=<2>`)
	if !strings.Contains(out, `href="https://example.com/?a=1&amp;b=&lt;2&gt;"`) {
		t.Fatalf("href not escaped: %s", out)
	}
	if strings.Contains(out, "<now>") {
		t.Fatalf("label not escaped: %s", out)
	}
}

func TestExpiryPhrase(t *testing.T) {
	now := time.Now()
	for want, at := range map[string]time.Time{
		"in 7 days":   now.Add(7 * 24 * time.Hour),
		"in 1 hour":   now.Add(90 * time.Minute),
		"in 36 hours": now.Add(36 * time.Hour),
		"immediately": now.Add(-time.Minute),
	} {
		if got := ExpiryPhrase(at, now); got != want {
			t.Errorf("ExpiryPhrase(%v) = %q, want %q", at.Sub(now), got, want)
		}
	}
}
