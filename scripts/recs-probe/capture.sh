#!/usr/bin/env bash
# Capture what /api/v2 recommends to the profiles of one account and print a
# one-line summary per check. It only sends GET requests.
#
# Usage: SILO_URL=https://silo.example SILO_TOKEN=... scripts/recs-probe/capture.sh [profile-id...]
#
#   SILO_URL    server base URL, without /api/v2
#   SILO_TOKEN  session token or API key of the account under test
#   OUT_DIR     where the JSON goes; default recs-probe-<UTC time>
#   PREV_DIR    an earlier capture of the same profiles, for day-over-day lines
#
# With no profile IDs it captures every profile of the account that has no
# PIN. The lines are measurements, not verdicts: the metrics are defined in
# docs/architecture/recommendations-evaluation.md.
set -euo pipefail

usage() {
	sed -n '2,14p' "$0" | sed 's/^# \{0,1\}//' >&2
}

case "${1:-}" in
	-h | --help)
		usage
		exit 0
		;;
esac

: "${SILO_URL:?set SILO_URL (see --help)}"
: "${SILO_TOKEN:?set SILO_TOKEN (see --help)}"
for tool in curl jq; do
	command -v "$tool" >/dev/null || {
		echo "capture: $tool is required" >&2
		exit 2
	}
done

base=${SILO_URL%/}
out=${OUT_DIR:-recs-probe-$(date -u +%Y%m%dT%H%M%SZ)}
prev=${PREV_DIR:-}
mkdir -p "$out"

# get PATH FILE [PROFILE] writes the response body to FILE and fails on a
# non-2xx answer. The token goes to curl on stdin, not on its command line.
get() {
	local path=$1 file=$2 profile=${3:-} code
	local headers=(-H 'Accept: application/json')
	if [[ -n "$profile" ]]; then
		headers+=(-H "X-Profile-Id: $profile")
	fi
	code=$(printf 'header = "Authorization: Bearer %s"\n' "$SILO_TOKEN" |
		curl -sS -K - "${headers[@]}" -o "$file" -w '%{http_code}' "$base$path") || code=000
	if [[ "$code" != 2* ]]; then
		echo "GET $path: HTTP $code (body in $file)" >&2
		return 1
	fi
}

get /api/v2/profiles "$out/profiles.json"
profiles=()
if (($# > 0)); then
	profiles=("$@")
else
	while IFS= read -r pid; do
		profiles+=("$pid")
	done < <(jq -r '.items[] | select(.has_pin | not) | .id' "$out/profiles.json")
	jq -r '.items[] | select(.has_pin) | "skipped \(.id) (\(.name)): PIN-locked profiles need a profile token"' "$out/profiles.json" >&2
fi
if ((${#profiles[@]} == 0)); then
	echo "capture: no profiles to capture" >&2
	exit 1
fi

# The recommendation sections a home page can carry.
rec_sections='["recommended_for_you","because_you_watched","similar_users_liked","taste_match"]'

# cards FILE... prints every recommendation card of the captured surfaces as
# one JSON object per line, tagged with its surface and row. Upcoming-airing
# cards on Discover are announcements, not recommendations, and are left out.
cards() {
	local dir=$1
	jq -c '{surface: "main", row: .title, type: .type} + (.items[] | {card: .})' "$dir/main.json"
	jq -c '.items[] | . as $r | {surface: "rows", row: $r.title, type: $r.type} + ($r.items[] | {card: .})' "$dir/rows.json"
	jq -c '.items[] | . as $r | $r.items[] | select(.upcoming_event == null) | {surface: "discover", row: $r.title, type: $r.type, card: .}' "$dir/discover.json"
	jq -c --argjson rec "$rec_sections" '.sections[] | select(.section_type | IN($rec[])) | . as $s | $s.items[] | {surface: "home", row: $s.title, type: $s.section_type, card: .}' "$dir/home.json"
}

for pid in "${profiles[@]}"; do
	dir="$out/$pid"
	mkdir -p "$dir"
	name=$(jq -r --arg id "$pid" '.items[] | select(.id == $id) | .name // empty' "$out/profiles.json")
	label="${name:-$pid}"

	ok=1
	get /api/v2/recommendations/taste-profile "$dir/taste.json" "$pid" || ok=0
	get "/api/v2/recommendations/for-you/main?limit=20" "$dir/main.json" "$pid" || ok=0
	get "/api/v2/recommendations/for-you/rows?limit=20" "$dir/rows.json" "$pid" || ok=0
	get /api/v2/recommendations/discover "$dir/discover.json" "$pid" || ok=0
	get /api/v2/home/sections "$dir/home.json" "$pid" || ok=0
	get "/api/v2/history?limit=200" "$dir/history.json" "$pid" || ok=0
	get "/api/v2/favorites?limit=200" "$dir/favorites.json" "$pid" || ok=0
	get "/api/v2/ratings?limit=200" "$dir/ratings.json" "$pid" || ok=0
	if ((ok == 0)); then
		echo "[$label] capture incomplete; summaries skipped" >&2
		continue
	fi
	cards "$dir" >"$dir/cards.jsonl"

	echo "[$label] profile $pid"
	jq -r '"  signals: positive=\([.signal_counts | to_entries[] | select(.key != "rated_low" and .key != "watch_low") | .value] | add // 0) counts=\(.signal_counts | tostring) updated_at=\(.updated_at // "never")"' "$dir/taste.json"
	jq -r '"  main: type=\(.type) title=\(.title | @json) cards=\(.items | length)"' "$dir/main.json"
	jq -r '"  discover: rows=\(.items | length) [\([.items[] | "\(.type):\(.title)=\(.items | length)"] | join(", "))]"' "$dir/discover.json"
	jq -r '"  discover rows with 10+ cards: \([.items[] | select((.items | length) >= 10)] | length)"' "$dir/discover.json"
	jq -r --argjson rec "$rec_sections" '"  home sections: \([.sections[] | select(.section_type | IN($rec[])) | "\(.section_type)=\(.items | length)"] | join(", "))"' "$dir/home.json"

	# Leakage: cards the profile finished, favorited or rated 1-2, by the
	# card's own flags and by the captured history, favorites and ratings
	# pages. A "+" marks a page with more entries than were captured.
	jq -rs --slurpfile h "$dir/history.json" --slurpfile f "$dir/favorites.json" --slurpfile r "$dir/ratings.json" '
		def more(p): "\(p.items | length)\(if p.page.has_more then "+" else "" end)";
		([$h[0].items[] | select(.watch.completed == true) | .content_id] | unique) as $finished
		| ([$f[0].items[].content_id] | unique) as $favorites
		| ([$r[0].items[] | select(.rating <= 2) | .item_id] | unique) as $low
		| "  leakage: finished=\([.[] | select(.card.content_id | IN($finished[]))] | length)"
		+ " favorited=\([.[] | select(.card.content_id | IN($favorites[]))] | length)"
		+ " rated_low=\([.[] | select(.card.content_id | IN($low[]))] | length)"
		+ " flagged_played=\([.[] | select(.card.user_state.played == true)] | length)"
		+ " flagged_favorite=\([.[] | select(.card.user_state.is_favorite == true)] | length)"
		+ " no_user_state=\([.[] | select(.card.user_state == null)] | length)"
		+ " pages: history=\(more($h[0])) favorites=\(more($f[0])) ratings=\(more($r[0]))"' "$dir/cards.jsonl"

	# Repetition within one load.
	jq -rs '
		def dupes(s): [.[] | select(.surface == s) | .card.content_id] | group_by(.) | map(select(length > 1)) | length;
		"  repeats: discover_cards_in_2+_rows=\(dupes("discover")) home_cards_in_2+_sections=\(dupes("home"))"' "$dir/cards.jsonl"

	# Card types in the personal rows (main, cluster and Similar Users rows).
	jq -rs '"  personal card types: \([.[] | select(.surface != "home" and (.type == "cluster" or .type == "similar_users_liked")) | .card.type] | group_by(.) | map("\(.[0])=\(length)") | join(", "))"' "$dir/cards.jsonl"

	# Genre shares: the served main row against the captured history and
	# favorites, each item counted once. amplification is the served share of
	# the row's top genre minus that genre's history share.
	jq -rn --slurpfile m "$dir/main.json" --slurpfile h "$dir/history.json" --slurpfile f "$dir/favorites.json" '
		def shares(items): (items | length) as $n
			| if $n == 0 then [] else [items[].genres // [] | unique[]] | group_by(.) | map({g: .[0], s: (length / $n)}) | sort_by(-.s, .g) end;
		def fmt(xs): [xs[:3][] | "\(.g)=\(.s * 100 | round)%"] | join(", ");
		shares($m[0].items) as $served
		| shares([$h[0].items[], $f[0].items[]] | unique_by(.content_id)) as $hist
		| ($served[0].g // null) as $top
		| "  genres: served[\(fmt($served))] history[\(fmt($hist))]"
		+ (if $top then " amplification(\($top))=\((($served[0].s) - ([$hist[] | select(.g == $top) | .s][0] // 0)) * 100 | round)pp" else "" end)'
done

# Overlap between profiles of the account: the served main rows and the
# union of each profile's personal rows.
captured=()
for pid in "${profiles[@]}"; do
	[[ -s "$out/$pid/cards.jsonl" ]] && captured+=("$pid")
done
for ((i = 0; i < ${#captured[@]}; i++)); do
	for ((j = i + 1; j < ${#captured[@]}; j++)); do
		a=${captured[i]} b=${captured[j]}
		jq -rn --arg a "$a" --arg b "$b" \
			--slurpfile ma "$out/$a/main.json" --slurpfile mb "$out/$b/main.json" \
			--slurpfile ca <(jq -s . "$out/$a/cards.jsonl") --slurpfile cb <(jq -s . "$out/$b/cards.jsonl") '
			def jac(x; y): (x | unique) as $x | (y | unique) as $y
				| ([$x[] | select(IN($y[]))] | length) as $i | ($x + $y | unique | length) as $u
				| "shared=\($i) jaccard=\(if $u == 0 then "n/a" else ($i / $u * 100 | round / 100) end)";
			def personal(c): [c[] | select(.surface != "home" and (.type == "cluster" or .type == "similar_users_liked")) | .card.content_id];
			"overlap \($a) vs \($b): main[\(jac([$ma[0].items[].content_id]; [$mb[0].items[].content_id]))] personal[\(jac(personal($ca[0]); personal($cb[0])))]"'
	done
done

# Day-over-day: the same profile's served main row against an earlier capture.
if [[ -n "$prev" ]] && ((${#captured[@]} > 0)); then
	for pid in "${captured[@]}"; do
		[[ -s "$prev/$pid/main.json" ]] || continue
		jq -rn --arg p "$pid" --slurpfile old "$prev/$pid/main.json" --slurpfile new "$out/$pid/main.json" '
			def ids(r; n): [r.items[:n][].content_id];
			def shared(n): (ids($old[0]; n)) as $o | [ids($new[0]; n)[] | select(IN($o[]))] | length;
			"repeat \($p) vs \($ENV.PREV_DIR): top10_shared=\(shared(10)) top20_shared=\(shared(20)) cards_then=\($old[0].items | length) cards_now=\($new[0].items | length)"'
	done
fi

echo "captured to $out" >&2
