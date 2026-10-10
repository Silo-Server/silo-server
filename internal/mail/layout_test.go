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

func TestRenderLayoutShowsBrandLogo(t *testing.T) {
	out := RenderLayout(LayoutOptions{Brand: Brand{Name: `Rock & "Roll"`}, BodyHTML: "x"})
	if !strings.Contains(out, `alt="Rock &amp; &#34;Roll&#34;"`) {
		t.Fatalf("server name not escaped into alt text:\n%s", out)
	}
	// The default Silo wordmark (191×100) fits the 48px-high header box.
	if !strings.Contains(out, `width="92" height="48"`) {
		t.Fatalf("default logo display size missing:\n%s", out)
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
	out := EmailButton(Brand{}, `Click "here" <now>`, `https://example.com/?a=1&b=<2>`)
	if !strings.Contains(out, `href="https://example.com/?a=1&amp;b=&lt;2&gt;"`) {
		t.Fatalf("href not escaped: %s", out)
	}
	if strings.Contains(out, "<now>") {
		t.Fatalf("label not escaped: %s", out)
	}
}

func TestEmailButtonUsesAccent(t *testing.T) {
	cases := []struct{ accent, background, label string }{
		{"", EmailColorAction, EmailColorOnAct},
		{"#f5a524", "#f5a524", EmailColorOnAct},             // light accent, dark label
		{"#1d4ed8", "#1d4ed8", "#ffffff"},                   // dark accent, white label
		{"red;x:url(y)", EmailColorAction, EmailColorOnAct}, // invalid: default
	}
	for _, tc := range cases {
		out := EmailButton(Brand{AccentColor: tc.accent}, "Go", "https://example.com")
		if !strings.Contains(out, `bgcolor="`+tc.background+`"`) ||
			!strings.Contains(out, `;color:`+tc.label+`;`) {
			t.Fatalf("accent %q: want background %s and label %s:\n%s", tc.accent, tc.background, tc.label, out)
		}
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
