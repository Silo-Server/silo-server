package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/emailalerts"
	"github.com/Silo-Server/silo-server/internal/notifications"
)

// EmailAlertLinks is what the public email-alert pages need.
type EmailAlertLinks interface {
	RecipientByToken(ctx context.Context, token string) (notifications.EmailAlertRecipient, error)
	Settings(ctx context.Context) (emailalerts.Settings, error)
	UpdateRecipientChoices(ctx context.Context, rec notifications.EmailAlertRecipient) error
	PublicURL(ctx context.Context) string
}

// EmailAlertLinksHandler serves the pages behind links in alert emails: a
// manage page (the token in the link is the capability) and the hand-off
// that opens an item in the Silo app when it is installed.
type EmailAlertLinksHandler struct {
	alerts EmailAlertLinks
}

// NewEmailAlertLinksHandler creates an EmailAlertLinksHandler.
func NewEmailAlertLinksHandler(alerts EmailAlertLinks) *EmailAlertLinksHandler {
	return &EmailAlertLinksHandler{alerts: alerts}
}

// silo:// links name the official Android app; its package is the hand-off
// target on Android.
const emailAlertAndroidPackage = "org.siloserver.silo"

var emailAlertContentID = regexp.MustCompile(`^[A-Za-z0-9._:~-]{1,200}$`)

// HandleOpen handles GET /notifications/email/open?id=...: it opens the item
// in the app on phones that have it, and in the browser otherwise.
func (h *EmailAlertLinksHandler) HandleOpen(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	public := h.alerts.PublicURL(r.Context())
	if !emailAlertContentID.MatchString(id) || public == "" {
		writeEmailAlertPage(w, http.StatusBadRequest, "Link not valid", "This link doesn't point to anything on the server.", "")
		return
	}
	web := public + "/item/" + url.PathEscape(id)
	app := "silo://item/" + url.PathEscape(id)
	intent := "intent://item/" + url.PathEscape(id) + "#Intent;scheme=silo;package=" + emailAlertAndroidPackage +
		";S.browser_fallback_url=" + url.QueryEscape(web) + ";end"
	js := func(s string) string {
		b, _ := json.Marshal(s) // escapes <, > and & for a script context
		return string(b)
	}
	buttons := `<p><a class="btn" href="` + html.EscapeString(app) + `">Open in the Silo app</a> <a class="btn alt" href="` +
		html.EscapeString(web) + `">Open in browser</a></p>
<script>
(function () {
  var web = ` + js(web) + `, app = ` + js(app) + `, intent = ` + js(intent) + `;
  var ua = navigator.userAgent || "";
  if (/android/i.test(ua)) {
    window.location.replace(intent);
  } else if (/iphone|ipad|ipod/i.test(ua) || (navigator.platform === "MacIntel" && navigator.maxTouchPoints > 1)) {
    var timer = setTimeout(function () { window.location.replace(web); }, 1600);
    document.addEventListener("visibilitychange", function () { if (document.hidden) clearTimeout(timer); });
    window.location.href = app;
  } else {
    window.location.replace(web);
  }
})();
</script>`
	writeEmailAlertPage(w, http.StatusOK, "Opening in Silo…", "If nothing happens, choose where to open it.", buttons)
}

// HandleManage handles GET and POST /notifications/email/manage?token=...
func (h *EmailAlertLinksHandler) HandleManage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		if err := r.ParseForm(); err != nil {
			writeEmailAlertPage(w, http.StatusBadRequest, "Something went wrong", "The form couldn't be read.", "")
			return
		}
	}
	token := r.URL.Query().Get("token")
	if token == "" {
		token = r.PostForm.Get("token")
	}
	rec, err := h.alerts.RecipientByToken(ctx, token)
	if errors.Is(err, notifications.ErrEmailRecipientNotFound) {
		writeEmailAlertPage(w, http.StatusNotFound, "Link not valid", "This link isn't recognised.", "")
		return
	}
	if err != nil {
		writeEmailAlertPage(w, http.StatusInternalServerError, "Something went wrong", "Try the link again in a moment.", "")
		return
	}
	settings, err := h.alerts.Settings(ctx)
	if err != nil {
		writeEmailAlertPage(w, http.StatusInternalServerError, "Something went wrong", "Try the link again in a moment.", "")
		return
	}
	msg := ""
	if r.Method == http.MethodPost {
		form := r.PostForm
		next := rec
		next.Mode = form.Get("mode")
		if !emailalerts.ValidMode(next.Mode) {
			next.Mode = emailalerts.ModeInstant
		}
		if v := form.Get("max_age"); v != "" {
			n, _ := strconv.Atoi(v)
			next.MaxAgeHours = emailalerts.CleanMaxAge(n)
		}
		// Unticking both would read as "everything", so keep the previous
		// choice instead.
		if types := emailalerts.CleanTypes(form["types"]); len(types) == 2 {
			next.Types = nil
		} else if len(types) == 1 {
			next.Types = types
		}
		if rec.Kind == notifications.EmailRecipientProfile {
			if form.Get("content") == notifications.EmailContentAll {
				next.Content = notifications.EmailContentAll
			} else {
				next.Content = notifications.EmailContentFollowed
			}
		}
		next.Enabled = form.Get("enabled") == "on"
		if !next.Enabled {
			next.Mode = ""
		}
		if err := h.alerts.UpdateRecipientChoices(ctx, next); err != nil {
			writeEmailAlertPage(w, http.StatusInternalServerError, "Something went wrong", "Your choices couldn't be saved.", "")
			return
		}
		if fresh, err := h.alerts.RecipientByToken(ctx, token); err == nil {
			rec = fresh
		}
		msg = `<p class="ok">Saved.</p>`
	}
	writeEmailAlertPage(w, http.StatusOK, "Your email alerts", "Settings for "+rec.Email+".", msg+emailAlertManageForm(token, rec, settings))
}

func emailAlertManageForm(token string, r notifications.EmailAlertRecipient, s emailalerts.Settings) string {
	checked := func(b bool) string {
		if b {
			return " checked"
		}
		return ""
	}
	all := len(r.Types) == 0
	days := []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}
	var b strings.Builder
	b.WriteString(`<form method="post"><input type="hidden" name="token" value="` + html.EscapeString(token) + `">`)
	b.WriteString(`<label class="row"><input type="checkbox" name="enabled"` + checked(r.Enabled && r.Mode != "") + `> Send me alerts</label>`)
	if r.Kind == notifications.EmailRecipientProfile {
		b.WriteString(`<fieldset><legend>What to send</legend>`)
		b.WriteString(`<label class="row"><input type="radio" name="content" value="followed"` + checked(r.Content != notifications.EmailContentAll) + `> Shows I follow</label>`)
		b.WriteString(`<label class="row"><input type="radio" name="content" value="all"` + checked(r.Content == notifications.EmailContentAll) + `> Everything new I can watch</label>`)
		b.WriteString(`</fieldset>`)
	}
	b.WriteString(`<fieldset><legend>How often</legend>`)
	b.WriteString(`<label class="row"><input type="radio" name="mode" value="instant"` + checked(r.Mode == emailalerts.ModeInstant || r.Mode == "") + `> As soon as something is added</label>`)
	b.WriteString(fmt.Sprintf(`<label class="row"><input type="radio" name="mode" value="instant_daily"%s> As soon as something is added, plus a daily digest at %02d:00</label>`, checked(r.Mode == emailalerts.ModeInstantDaily), s.Schedule.DailyHour))
	b.WriteString(fmt.Sprintf(`<label class="row"><input type="radio" name="mode" value="instant_weekly"%s> As soon as something is added, plus a weekly digest on %s at %02d:00</label>`, checked(r.Mode == emailalerts.ModeInstantWeekly), days[s.Schedule.WeeklyDay], s.Schedule.WeeklyHour))
	b.WriteString(fmt.Sprintf(`<label class="row"><input type="radio" name="mode" value="daily"%s> Daily digest at %02d:00</label>`, checked(r.Mode == emailalerts.ModeDaily), s.Schedule.DailyHour))
	b.WriteString(fmt.Sprintf(`<label class="row"><input type="radio" name="mode" value="weekly"%s> Weekly digest on %s at %02d:00</label>`, checked(r.Mode == emailalerts.ModeWeekly), days[s.Schedule.WeeklyDay], s.Schedule.WeeklyHour))
	b.WriteString(`</fieldset><fieldset><legend>Count as new if added within</legend><select name="max_age">`)
	for _, o := range emailalerts.MaxAgeOptions(s) {
		sel := ""
		if o.Hours == r.MaxAgeHours {
			sel = " selected"
		}
		b.WriteString(fmt.Sprintf(`<option value="%d"%s>%s</option>`, o.Hours, sel, html.EscapeString(o.Label)))
	}
	b.WriteString(`</select></fieldset><fieldset><legend>What to include</legend>`)
	b.WriteString(`<label class="row"><input type="checkbox" name="types" value="episode"` + checked(all || slices.Contains(r.Types, "episode")) + `> TV episodes</label>`)
	b.WriteString(`<label class="row"><input type="checkbox" name="types" value="movie"` + checked(all || slices.Contains(r.Types, "movie")) + `> Movies</label>`)
	b.WriteString(`</fieldset><button type="submit">Save</button></form>`)
	return b.String()
}

// writeEmailAlertPage renders the standalone pages behind alert email links,
// following the reader's light or dark preference.
func writeEmailAlertPage(w http.ResponseWriter, status int, title, message, extra string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(status)
	body := `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>` + html.EscapeString(title) + ` — Silo</title>
<style>
:root{color-scheme:light dark;--bg:#f6f7f9;--card:#fff;--text:#16181d;--muted:#5d6470;--accent:#6b4dff;--line:#e3e6ea}
@media (prefers-color-scheme:dark){:root{--bg:#0f1115;--card:#181b22;--text:#f2f3f5;--muted:#9aa0ab;--line:#2a2f39}}
body{margin:0;background:var(--bg);color:var(--text);font:15px/1.55 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif;padding:40px 16px}
main{max-width:520px;margin:0 auto;background:var(--card);border:1px solid var(--line);border-radius:14px;padding:28px}
h1{font-size:22px;margin:0 0 8px}p{color:var(--muted);margin:0 0 18px}
button,.btn{display:inline-block;background:var(--accent);color:#fff;border:0;border-radius:8px;padding:10px 18px;font:inherit;font-weight:600;cursor:pointer;text-decoration:none}
.btn.alt{background:transparent;color:var(--text);border:1px solid var(--line)}
fieldset{border:1px solid var(--line);border-radius:10px;margin:0 0 16px;padding:10px 14px}legend{color:var(--muted);font-size:13px;padding:0 6px}
select{width:100%;background:var(--bg);color:var(--text);border:1px solid var(--line);border-radius:8px;padding:8px 10px;font:inherit;margin:5px 0}
.row{display:flex;gap:10px;align-items:center;padding:5px 0}.ok{color:#2e9d5b;font-weight:600}
</style></head><body><main><h1>` + html.EscapeString(title) + `</h1>`
	if message != "" {
		body += `<p>` + html.EscapeString(message) + `</p>`
	}
	body += extra + `</main></body></html>`
	_, _ = w.Write([]byte(body))
}
