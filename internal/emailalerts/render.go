package emailalerts

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cbroglie/mustache"
)

// Entry is one new item with the context needed to render it.
type Entry struct {
	Card    Card
	Library LibraryRef
	Series  *Detail // nil for movies, or when the series could not be read
	Episode *Detail // episode detail (still, air date) when available
	Movie   *Detail // movie detail (tagline, cast) when available
}

type LibraryRef struct {
	ID   string
	Name string
}

// RenderEnv carries everything outside the items that templates can use.
type RenderEnv struct {
	Settings       Settings
	ServerName     string
	PublicURL      string
	InternalBase   string
	RecipientName  string
	RecipientEmail string
	Mode           string // instant | daily | weekly | test | preview
	Now            time.Time
	ManageURL      string
	UnsubscribeURL string
	MoreCount      int
	// OpenURL is the public page that opens an item in the Silo app when it
	// is installed (Theme.OpenInApp); it takes the content id as ?id=.
	OpenURL string
	// BackdropBG returns the URL of a series' darkened backdrop for use as a
	// CSS background, or "".
	BackdropBG func(seriesID string) string
	// Preview renders image references as browser-loadable URLs instead of
	// cid: references.
	Preview bool
}

// Rendered is a finished email.
type Rendered struct {
	Subject string
	HTML    string
	Text    string
	Images  []EmbeddedImage
}

type EmbeddedImage struct {
	CID         string
	ContentType string
	Data        []byte
}

// imageSet hands out stable references for artwork URLs.
type imageSet struct {
	preview      bool
	internalBase string
	byURL        map[string]string
	byCID        map[string]string
	blocked      map[string]bool
	n            int
}

func newImageSet(preview bool, internalBase string) *imageSet {
	return &imageSet{preview: preview, internalBase: internalBase, byURL: map[string]string{}, byCID: map[string]string{}, blocked: map[string]bool{}}
}

func (s *imageSet) ref(raw string) string {
	if raw == "" || s.blocked[raw] {
		return ""
	}
	if s.preview {
		// The admin's browser is on Silo's own origin, so a path relative to
		// the server loads there even when the API handed out an internal URL.
		if s.internalBase != "" && strings.HasPrefix(raw, s.internalBase) {
			return strings.TrimPrefix(raw, s.internalBase)
		}
		return raw
	}
	if cid, ok := s.byURL[raw]; ok {
		return "cid:" + cid
	}
	s.n++
	cid := fmt.Sprintf("img%d@silo-email", s.n)
	s.byURL[raw] = cid
	s.byCID[cid] = raw
	return "cid:" + cid
}

var cidPattern = regexp.MustCompile(`cid:(img\d+@silo-email)`)

// Render produces the email for entries. Images are fetched with fetch;
// pass nil to skip embedding.
func Render(ctx context.Context, env RenderEnv, entries []Entry, fetch func(context.Context, string) ([]byte, string, error)) (*Rendered, error) {
	embed := env.Settings.Delivery.EmbedImages && fetch != nil && !env.Preview
	// Without embedding, presigned artwork links would expire before the
	// email is read, so artwork variables are left empty.
	withImages := embed || env.Preview
	images := newImageSet(env.Preview, env.InternalBase)
	cache := map[string]EmbeddedImage{}

	var out *Rendered
	for attempt := 0; attempt < 3; attempt++ {
		r, err := renderTemplates(env.Settings, buildData(env, entries, images, withImages))
		if err != nil {
			return nil, err
		}
		out = r
		if !embed {
			return out, nil
		}
		var failed []string
		for i, cid := range uniqueCIDs(out.HTML) {
			raw := images.byCID[cid]
			if img, ok := cache[cid]; ok {
				out.Images = append(out.Images, img)
				continue
			}
			if i >= env.Settings.Delivery.MaxImages {
				failed = append(failed, raw)
				continue
			}
			data, ct, err := fetch(ctx, raw)
			if err != nil {
				failed = append(failed, raw)
				continue
			}
			img := EmbeddedImage{CID: cid, ContentType: ct, Data: data}
			cache[cid] = img
			out.Images = append(out.Images, img)
		}
		if len(failed) == 0 {
			return out, nil
		}
		// Re-render with the missing images removed, so {{#poster}} sections
		// collapse instead of showing broken images.
		for _, raw := range failed {
			images.blocked[raw] = true
		}
		out.Images = nil
	}
	return out, nil
}

func uniqueCIDs(html string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range cidPattern.FindAllStringSubmatch(html, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

func renderTemplates(settings Settings, data map[string]any) (*Rendered, error) {
	subject, err := renderOne("subject", settings.Templates.Subject, true, data)
	if err != nil {
		return nil, err
	}
	html, err := renderOne("HTML", settings.Templates.HTML, false, data)
	if err != nil {
		return nil, err
	}
	text, err := renderOne("plain text", settings.Templates.Text, true, data)
	if err != nil {
		return nil, err
	}
	subject = strings.Join(strings.Fields(subject), " ")
	if subject == "" {
		subject = "New on " + fmt.Sprint(data["server_name"])
	}
	return &Rendered{Subject: subject, HTML: html, Text: collapseBlankLines(text)}, nil
}

type TemplateError struct {
	Part string
	Err  error
}

func (e *TemplateError) Error() string { return fmt.Sprintf("%s template: %v", e.Part, e.Err) }

func renderOne(part, src string, raw bool, data map[string]any) (string, error) {
	tmpl, err := mustache.ParseStringRaw(src, raw)
	if err != nil {
		return "", &TemplateError{Part: part, Err: err}
	}
	out, err := tmpl.Render(data)
	if err != nil {
		return "", &TemplateError{Part: part, Err: err}
	}
	return out, nil
}

var blankLines = regexp.MustCompile(`\n{3,}`)

func collapseBlankLines(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	return strings.TrimSpace(blankLines.ReplaceAllString(strings.Join(lines, "\n"), "\n\n")) + "\n"
}

// renderInline renders a short theme string (which may itself use
// variables, such as "New on {{server_name}}") as raw text.
func renderInline(src string, data map[string]any) string {
	if !strings.Contains(src, "{{") {
		return src
	}
	out, err := mustache.RenderRaw(src, true, data)
	if err != nil {
		return src
	}
	return out
}

func buildData(env RenderEnv, entries []Entry, images *imageSet, withImages bool) map[string]any {
	s := env.Settings
	loc := s.Location()
	now := env.Now.In(loc)
	img := func(raw string) string {
		if !withImages {
			return ""
		}
		return images.ref(raw)
	}

	var (
		items    []map[string]any
		movies   []map[string]any
		episodes []map[string]any
		showIdx  = map[string]int{}
		shows    []map[string]any
		showEps  [][]map[string]any
	)
	for _, e := range entries {
		vars := itemVars(env, e, img, loc)
		items = append(items, vars)
		if e.Card.Type == "episode" {
			episodes = append(episodes, vars)
			key := e.Card.SeriesID
			if key == "" {
				key = e.Card.SeriesTitle
			}
			i, ok := showIdx[key]
			if !ok {
				i = len(shows)
				showIdx[key] = i
				shows = append(shows, showVars(env, e, img))
				showEps = append(showEps, nil)
			}
			showEps[i] = append(showEps[i], vars)
		} else {
			movies = append(movies, vars)
		}
	}
	for i, show := range shows {
		eps := showEps[i]
		sort.SliceStable(eps, func(a, b int) bool {
			sa, sb := eps[a]["season_number"].(int), eps[b]["season_number"].(int)
			if sa != sb {
				return sa < sb
			}
			return eps[a]["episode_number"].(int) < eps[b]["episode_number"].(int)
		})
		for j, ep := range eps {
			ep["is_first"] = j == 0
			ep["is_last"] = j == len(eps)-1
		}
		show["episodes"] = eps
		show["episode_count"] = len(eps)
		show["single_episode"] = len(eps) == 1
		show["multiple_episodes"] = len(eps) > 1
		show["episodes_label"] = plural(len(eps), "new episode", "new episodes")
		show["episode_range"] = episodeRange(eps)
		first := eps[0]
		for _, k := range []string{"episode_name", "episode_code", "episode_synopsis", "episode_image", "season_number", "episode_number", "url", "air_date", "runtime"} {
			show["first_"+k] = first[k]
		}
	}

	period := map[string]string{
		"instant": "just added", "daily": "today", "weekly": "this week", "test": "recently", "preview": "recently",
	}[env.Mode]

	data := map[string]any{
		"server_name":          env.ServerName,
		"server_url":           env.PublicURL,
		"date":                 now.Format("Monday 2 January 2006"),
		"short_date":           now.Format("2 Jan"),
		"time":                 now.Format("15:04"),
		"year_now":             now.Year(),
		"mode":                 env.Mode,
		"period":               period,
		"is_instant":           env.Mode == "instant",
		"is_digest":            env.Mode == "daily" || env.Mode == "weekly",
		"is_daily":             env.Mode == "daily",
		"is_weekly":            env.Mode == "weekly",
		"is_test":              env.Mode == "test" || env.Mode == "preview",
		"recipient_name":       env.RecipientName,
		"recipient_first_name": firstName(env.RecipientName),
		"recipient_email":      env.RecipientEmail,
		"items":                items,
		"movies":               movies,
		"episodes":             episodes,
		"shows":                shows,
		"total_count":          len(items),
		"movie_count":          len(movies),
		"episode_count":        len(episodes),
		"show_count":           len(shows),
		"has_items":            len(items) > 0,
		"has_movies":           len(movies) > 0,
		"has_shows":            len(shows) > 0,
		"has_movies_and_shows": len(movies) > 0 && len(shows) > 0,
		"single_item":          len(items) == 1,
		"summary":              summary(len(movies), len(episodes), len(shows)),
		"more_count":           env.MoreCount,
		"has_more":             env.MoreCount > 0,
		"manage_url":           env.ManageURL,
		"unsubscribe_url":      env.UnsubscribeURL,
		"accent_color":         s.Theme.AccentColor,
		"background_color":     s.Theme.BackgroundColor,
		"card_color":           s.Theme.CardColor,
		"text_color":           s.Theme.TextColor,
		"muted_color":          s.Theme.MutedColor,
		"logo_url":             s.Theme.LogoURL,
		"button_text":          s.Theme.ButtonText,
	}
	if len(items) > 0 {
		data["first_title"] = items[0]["title"]
		data["first_display_title"] = items[0]["display_title"]
		data["first_show_name"] = items[0]["show_name"]
	}
	if len(shows) > 0 {
		data["first_show_name"] = shows[0]["show_name"]
	}
	if len(movies) > 0 {
		data["first_movie_title"] = movies[0]["title"]
	}
	custom := map[string]any{}
	for _, v := range s.CustomVars {
		custom[v.Key] = v.Value
		if _, taken := data[v.Key]; !taken {
			data[v.Key] = v.Value
		}
	}
	data["custom"] = custom
	// Theme strings may use variables themselves.
	data["header_title"] = renderInline(s.Theme.HeaderTitle, data)
	data["intro"] = renderInline(s.Theme.Intro, data)
	data["footer_text"] = renderInline(s.Theme.FooterText, data)
	return data
}

func itemVars(env RenderEnv, e Entry, img func(string) string, loc *time.Location) map[string]any {
	c := e.Card
	limit := env.Settings.Delivery.SynopsisLength
	v := map[string]any{
		"content_id":      c.ContentID,
		"type":            c.Type,
		"is_movie":        c.Type == "movie",
		"is_episode":      c.Type == "episode",
		"title":           c.Title,
		"year":            yearOrEmpty(c.Year),
		"synopsis":        truncate(c.Overview, limit),
		"synopsis_full":   c.Overview,
		"has_synopsis":    strings.TrimSpace(c.Overview) != "",
		"genres":          strings.Join(c.Genres, ", "),
		"genre_list":      nameList(c.Genres),
		"rating":          ratingString(c.RatingTMDB),
		"imdb_rating":     ratingString(c.RatingIMDB),
		"content_rating":  c.ContentRating,
		"runtime":         runtimeString(c.Runtime, c.DurationSeconds),
		"runtime_minutes": runtimeMinutes(c.Runtime, c.DurationSeconds),
		"release_date":    humanDate(c.ReleaseDate),
		"library_name":    e.Library.Name,
		"library_id":      e.Library.ID,
		"url":             env.link(c.ContentID),
		"web_url":         itemURL(env.PublicURL, c.ContentID),
		"added_at":        c.AddedAt.Format(time.RFC3339),
		"added_date":      c.added().In(loc).Format("Mon 2 Jan, 15:04"),
		"studios":         strings.Join(c.Studios, ", "),
		"season_number":   0,
		"episode_number":  0,
		"poster":          "",
		"backdrop":        "",
		"has_poster":      false,
		"has_backdrop":    false,
	}
	if c.Type == "episode" {
		season, episode := 0, 0
		if c.SeasonNumber != nil {
			season = *c.SeasonNumber
		}
		if c.EpisodeNumber != nil {
			episode = *c.EpisodeNumber
		}
		still := ""
		if c.BackdropIsEpisodeStill {
			still = c.BackdropURL
		} else if c.PosterIsEpisodeStill {
			still = c.PosterURL
		}
		airDate, runtime := c.ReleaseDate, runtimeString(c.Runtime, c.DurationSeconds)
		if d := e.Episode; d != nil {
			if still == "" && d.PosterIsEpisodeStill {
				still = d.PosterURL
			}
			if still == "" && d.BackdropIsEpisodeStill {
				still = d.BackdropURL
			}
			if d.AirDate != "" {
				airDate = d.AirDate
			}
			if runtime == "" {
				runtime = runtimeString(d.Runtime, d.DurationSeconds)
			}
		}
		showName := c.SeriesTitle
		showSynopsis, showPoster, showBackdrop, network, showGenres := "", "", "", "", ""
		if e.Series != nil {
			if showName == "" {
				showName = e.Series.Title
			}
			showSynopsis = e.Series.Overview
			showPoster = e.Series.PosterURL
			showBackdrop = e.Series.BackdropURL
			network = strings.Join(e.Series.Networks, ", ")
			showGenres = strings.Join(e.Series.Genres, ", ")
		}
		if showPoster == "" && !c.PosterIsEpisodeStill {
			showPoster = c.PosterURL
		}
		epImage := img(still)
		v["show_name"] = showName
		v["show_synopsis"] = truncate(showSynopsis, limit)
		v["show_synopsis_full"] = showSynopsis
		v["show_url"] = env.link(c.SeriesID)
		v["show_web_url"] = itemURL(env.PublicURL, c.SeriesID)
		v["show_poster"] = img(showPoster)
		v["show_backdrop"] = img(showBackdrop)
		v["network"] = network
		v["show_genres"] = showGenres
		v["season_number"] = season
		v["episode_number"] = episode
		v["episode_code"] = episodeCode(season, episode)
		v["season_label"] = seasonLabel(season)
		v["episode_name"] = c.Title
		v["episode_synopsis"] = truncate(c.Overview, limit)
		v["episode_synopsis_full"] = c.Overview
		v["episode_image"] = epImage
		v["has_episode_image"] = epImage != ""
		v["air_date"] = humanDate(airDate)
		v["runtime"] = runtime
		v["poster"] = v["show_poster"]
		v["has_poster"] = v["show_poster"] != ""
		if epImage != "" {
			v["backdrop"] = epImage
		} else {
			v["backdrop"] = v["show_backdrop"]
		}
		v["has_backdrop"] = v["backdrop"] != ""
		v["display_title"] = fmt.Sprintf("%s · %s · %s", showName, episodeCode(season, episode), c.Title)
	} else {
		poster := img(c.PosterURL)
		backdrop := img(c.BackdropURL)
		v["poster"] = poster
		v["backdrop"] = backdrop
		v["has_poster"] = poster != ""
		v["has_backdrop"] = backdrop != ""
		// The darkened backdrop behind the movie card, like a show's.
		bg := ""
		if env.BackdropBG != nil && c.ContentID != "" && c.BackdropURL != "" {
			bg = env.BackdropBG(c.ContentID)
		}
		v["backdrop_bg"] = bg
		v["display_title"] = strings.TrimSpace(fmt.Sprintf("%s %s", c.Title, parenYear(c.Year)))
		if e.Movie != nil {
			v["tagline"] = e.Movie.Tagline
			v["cast"] = castString(e.Movie.Cast, 4)
		}
	}
	return v
}

func showVars(env RenderEnv, e Entry, img func(string) string) map[string]any {
	c := e.Card
	limit := env.Settings.Delivery.SynopsisLength
	v := map[string]any{
		"show_name":     c.SeriesTitle,
		"show_url":      env.link(c.SeriesID),
		"show_web_url":  itemURL(env.PublicURL, c.SeriesID),
		"series_id":     c.SeriesID,
		"library_name":  e.Library.Name,
		"show_synopsis": "",
		"show_poster":   "",
		"show_backdrop": "",
	}
	if e.Series != nil {
		d := e.Series
		if v["show_name"] == "" {
			v["show_name"] = d.Title
		}
		v["show_synopsis"] = truncate(d.Overview, limit)
		v["show_synopsis_full"] = d.Overview
		v["has_show_synopsis"] = strings.TrimSpace(d.Overview) != ""
		v["show_poster"] = img(d.PosterURL)
		v["show_backdrop"] = img(d.BackdropURL)
		v["show_year"] = yearOrEmpty(d.Year)
		v["show_genres"] = strings.Join(d.Genres, ", ")
		v["show_genre_list"] = nameList(d.Genres)
		v["network"] = strings.Join(d.Networks, ", ")
		v["show_rating"] = ratingString(d.RatingTMDB)
		v["show_status"] = d.ShowStatus
		v["show_content_rating"] = d.ContentRating
		v["show_tagline"] = d.Tagline
		v["season_count"] = d.SeasonCount
		v["cast"] = castString(d.Cast, 4)
	} else if !c.PosterIsEpisodeStill {
		v["show_poster"] = img(c.PosterURL)
	}
	v["has_show_poster"] = v["show_poster"] != ""
	v["has_show_backdrop"] = v["show_backdrop"] != ""
	bg := ""
	if env.BackdropBG != nil && c.SeriesID != "" && (e.Series == nil || e.Series.BackdropURL != "") {
		bg = env.BackdropBG(c.SeriesID)
	}
	v["show_backdrop_bg"] = bg
	return v
}

var contentIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:~-]{1,200}$`)

// link returns the URL an email uses for a catalog item.
func (env RenderEnv) link(id string) string {
	if env.Settings.Theme.OpenInApp && env.OpenURL != "" && contentIDPattern.MatchString(id) {
		return env.OpenURL + "?id=" + url.QueryEscape(id)
	}
	return itemURL(env.PublicURL, id)
}

func itemURL(public, id string) string {
	if public == "" || id == "" {
		return ""
	}
	return public + "/item/" + id
}

func truncate(s string, limit int) string {
	s = strings.TrimSpace(s)
	if limit <= 0 || utf8.RuneCountInString(s) <= limit {
		return s
	}
	r := []rune(s)
	cut := string(r[:limit])
	if i := strings.LastIndexAny(cut, " \n"); i > limit/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,.;:") + "…"
}

func episodeCode(season, episode int) string {
	return fmt.Sprintf("S%02dE%02d", season, episode)
}

func seasonLabel(season int) string {
	if season == 0 {
		return "Specials"
	}
	return fmt.Sprintf("Season %d", season)
}

func episodeRange(eps []map[string]any) string {
	if len(eps) == 0 {
		return ""
	}
	first, last := eps[0], eps[len(eps)-1]
	if len(eps) == 1 {
		return first["episode_code"].(string)
	}
	if first["season_number"] == last["season_number"] {
		return fmt.Sprintf("%s–E%02d", first["episode_code"], last["episode_number"])
	}
	return fmt.Sprintf("%s–%s", first["episode_code"], last["episode_code"])
}

func ratingString(r float64) string {
	if r <= 0 {
		return ""
	}
	return fmt.Sprintf("%.1f", r)
}

func runtimeMinutes(minutes int, seconds float64) int {
	if minutes > 0 {
		return minutes
	}
	return int(seconds / 60)
}

func runtimeString(minutes int, seconds float64) string {
	m := runtimeMinutes(minutes, seconds)
	switch {
	case m <= 0:
		return ""
	case m < 60:
		return fmt.Sprintf("%dm", m)
	case m%60 == 0:
		return fmt.Sprintf("%dh", m/60)
	default:
		return fmt.Sprintf("%dh %dm", m/60, m%60)
	}
}

func humanDate(s string) string {
	if s == "" {
		return ""
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return s
	}
	return t.Format("2 January 2006")
}

func yearOrEmpty(y int) any {
	if y <= 0 {
		return ""
	}
	return y
}

func parenYear(y int) string {
	if y <= 0 {
		return ""
	}
	return fmt.Sprintf("(%d)", y)
}

func nameList(names []string) []map[string]any {
	out := make([]map[string]any, 0, len(names))
	for i, n := range names {
		out = append(out, map[string]any{"name": n, "is_last": i == len(names)-1})
	}
	return out
}

func castString(cast []CastMember, n int) string {
	var names []string
	for _, c := range cast {
		if c.Name != "" {
			names = append(names, c.Name)
		}
		if len(names) == n {
			break
		}
	}
	return strings.Join(names, ", ")
}

func firstName(name string) string {
	f := strings.Fields(name)
	if len(f) == 0 {
		return ""
	}
	return f[0]
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func summary(movies, episodes, shows int) string {
	var parts []string
	if episodes > 0 {
		if shows == 1 {
			parts = append(parts, plural(episodes, "new episode", "new episodes"))
		} else {
			parts = append(parts, fmt.Sprintf("%s across %d shows", plural(episodes, "new episode", "new episodes"), shows))
		}
	}
	if movies > 0 {
		parts = append(parts, plural(movies, "new movie", "new movies"))
	}
	if len(parts) == 0 {
		return "Nothing new"
	}
	return strings.Join(parts, " and ")
}
