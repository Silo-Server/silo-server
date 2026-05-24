# Audiobook UI Redesign — Design Spec

**Date:** 2026-05-24
**Branch context:** `feat/audiobooks` in `silo-server`
**Status:** Approved — ready for implementation plan
**Related spec:** [`2026-05-24-audiobooks-absorption-design.md`](./2026-05-24-audiobooks-absorption-design.md)

## Goal

Bring the audiobook detail page and audiobook player to visual and
interaction parity with Silo's existing video player, translated for
audio. Today the audiobook surfaces work but feel generic, hide useful
information, and re-implement primitives the video player already has
in a polished form.

User-visible outcome:

- The audiobook player has the same two-mode shape as the video player
  (compact HUD + immersive full-screen mode), so listening can be
  background or foreground depending on intent.
- The detail page foregrounds the information audiobook listeners care
  about — current chapter, narrator, listened progress — instead of
  burying it.
- The audiobook-only affordances that don't exist today (sleep timer,
  speed menu, bookmarks, narrator emphasis) have a home.

## Hard constraints

- **Reuse video-player primitives.** `SeekBar`, `ChaptersMenu`, and the
  glass-disc button visual treatment all live in `web/src/player/` and
  are already wired up correctly. The audiobook player must consume
  them, not re-implement them.
- **One audio element.** Switching between mini and Now Listening must
  not remount the audio element or restart the stream. Both modes are
  chrome layered over the same playback state.
- **No new top-level routes.** The Now Listening view is an overlay on
  whatever route the user is on, not a `/audiobooks/listen` page. This
  mirrors how fullscreen video works — fullscreen is a mode, not a
  destination.
- **Player state must be liftable.** The split between
  `useAudiobookPlayback` (state hook) and `MiniBar` / `NowListening`
  (chrome) must allow a future v1.1 change to lift the hook into an
  app-level provider so the mini bar can survive page navigation.
  v1 itself ships with the player tied to the detail route, same as
  today.

## Scope

### In

- New mini-bar layout (cover tile, chapter title, glass-disc transport,
  utility rail with sleep / chapters / speed / expand / close).
- New Now Listening full-overlay mode (large cover, chapter heading,
  tall seek bar, sleep / chapters / speed / bookmark / car-mode row,
  remaining-time toggle, overflow menu).
- Restructured audiobook detail page (progress + chapter-aware Resume
  action, chapters expanded by default with currently-playing
  highlight, narrator card, embedding-based "Similar audiobooks"
  rail, "Also by author", "In this series").
- Extraction of `CircleButton` and a new `SpeedMenu` into
  `web/src/player/components/` as shared primitives between video and
  audiobook players.
- Split of today's monolithic `AudiobookPlayer.tsx` into a state hook
  (`useAudiobookPlayback`) plus two chrome components (`MiniBar`,
  `NowListening`) under `web/src/pages/audiobooks/player/`.
- Sleep timer (client-side only; pauses + short fade-out when the
  timer fires).

### Out (deferred to v1.1)

- **Bookmarks.** UI affordances (mini-bar button, Now Listening
  utility-row button, "Bookmarks (n)" detail-page action) ship
  **hidden** in v1. Full feature needs `audiobook_bookmarks` table +
  CRUD endpoints, which is its own sub-spec.
- **"Also by narrator" / cross-book "In this series" rails.** Render
  only when the backend already exposes the data; otherwise hide.
  Server-side joins to power them are a follow-up.
- **Car mode.** Hidden in v1; v1.1 will add a huge-buttons layout
  variant reached from the utility row.
- **Audiobook library page redesign.** Out of scope; current grid
  stays.
- **Persistent player across page navigation as a lifted React
  context.** The current detail page owns the player state; lifting it
  to an app-level provider so the bar survives navigating away from
  the detail page is a follow-up. v1 ships with the bar tied to the
  detail route, same as today, but visually and structurally ready
  for that lift.

## Architecture

### Three connected surfaces

```
detail page  ──opens──►  mini bar  ──expand──►  Now Listening
                            ▲                       │
                            └─────── collapse ──────┘
```

- **Detail page** (`/audiobooks/book/:id`) is the landing surface.
  Stays mounted while the player is open.
- **Mini bar** is a fixed bottom strip (current behavior) that appears
  once the user clicks Play/Resume.
- **Now Listening** is a full-viewport overlay (z-index above the mini
  bar, below modals) reached by clicking the cover-art tile or the
  expand chevron in the mini bar. Dismissed via a collapse chevron.

The mini ↔ Now Listening transition uses a CSS view transition keyed
on the cover art element so the small tile appears to grow into the
big cover. The same primitive already used by `ViewTransitionLink`.

### Shared player primitives

`web/src/player/components/` already houses the visual building blocks
used by the video player. Two of them get extracted so both players
consume the same source of truth:

| File | Status | Purpose |
|---|---|---|
| `SeekBar.tsx` | reused as-is | already shared |
| `ChaptersMenu.tsx` | reused as-is | already shared |
| `CircleButton.tsx` | **new** — extracted from `PlayerControls.tsx` | the glass-disc primary/secondary button used in both transport clusters |
| `SpeedMenu.tsx` | **new** | popover speed menu matching the styling of `ChaptersMenu`; replaces the raw `<select>` today; video player can adopt later |
| `SleepTimerMenu.tsx` | **new** | popover with off / 5 / 15 / 30 / 45 / 60 / end-of-chapter; audiobook-only but lives here for symmetry |

Extracting `CircleButton` removes a divergence rather than adding a
layer — it's the kind of focused improvement that earns its keep
because today the video player and the audiobook player each render
their own visually-similar-but-not-identical disc buttons.

### Audiobook player component tree

```
web/src/pages/audiobooks/
  AudiobookDetail.tsx              (restructured per "Detail page" below)
  AudiobookLibrary.tsx             (unchanged)
  player/                          (new folder)
    AudiobookPlayer.tsx            (top level: owns audio element and state)
    MiniBar.tsx                    (compact chrome)
    NowListening.tsx               (overlay chrome)
    CoverExpandTile.tsx            (left-edge tile inside MiniBar — also the view-transition anchor)
    useAudiobookPlayback.ts        (state hook: audio events, progress reporting, sleep timer)
```

`AudiobookPlayer` becomes a thin shell:

```tsx
function AudiobookPlayer(props) {
  const playback = useAudiobookPlayback(props);
  const [mode, setMode] = useState<"mini" | "now-listening">("mini");
  return (
    <>
      <audio ref={playback.audioRef} src={playback.streamUrl} preload="metadata" hidden />
      {mode === "mini"
        ? <MiniBar playback={playback} onExpand={() => setMode("now-listening")} onClose={props.onClose} />
        : <NowListening playback={playback} onCollapse={() => setMode("mini")} />}
    </>
  );
}
```

The audio element lives in the parent so a mode swap never touches it.

### useAudiobookPlayback responsibilities

Lifts today's monolithic `AudiobookPlayer.tsx` into a single hook
returning a stable shape:

```ts
{
  audioRef,                       // ref<HTMLAudioElement>
  streamUrl,                      // string
  playing, currentTime, duration, buffered, rate,
  chapters,                       // PlayerChapter[] (flattened across files)
  currentChapter,                 // PlayerChapter | null
  sleep,                          // { remainingMs: number | null, end: SleepEndCondition | null }
  togglePlay, seekTo, skip,
  setRate,
  setSleep,                       // arms the timer
  // bookmarks (v1.1 — initially returns []/no-op stubs)
  bookmarks, addBookmark, removeBookmark,
}
```

The hook owns: audio event wiring, periodic progress reporting (the
existing 10s `useReportAudiobookProgress` cadence), sleep-timer state
+ fade-out, computing `currentChapter` from `currentTime`, and the
unmount-time pause + final report. The chrome components stay pure
presentational.

## Mini bar

Layout (one seek-bar row above a controls row):

```
[ full-width SeekBar with chapter ticks ]
[ cover tile | title + chapter + time | ◀30 ▶❚❚ 30▶ | ⌛ ☰ 1× ⌃ ✕ ]
   ↑ left col       middle              center           right rail
   36×54px          truncating          shared CircleButton cluster
                                                            sleep, chapters, speed,
                                                            expand chevron, close
                                                            (bookmark hidden in v1)
```

Differences from today's implementation:

- **CoverExpandTile** added at the left edge. 36×54 (portrait
  aspect-2:3). On hover: subtle scale + a small "expand" chevron
  overlay. Clicking it triggers the view transition to Now Listening.
- **Left text column** shows title (line 1) and current chapter title
  (line 2). Time row stays where it is. Today the chapter title isn't
  shown anywhere while playing — this is the single most useful
  "where am I" cue and it's missing.
- **Center cluster** swaps the bespoke `CircleButton` defined inline
  in the current `AudiobookPlayer` for the extracted shared one. Same
  three controls (back 30 / play-pause / forward 30).
- **Right rail** replaces the raw `<select>` for speed with
  `SpeedMenu` (popover, same styling as `ChaptersMenu`). Adds
  `SleepTimerMenu`, an expand-up chevron (`ChevronUp`), and the
  existing close `X`. The bookmark button is **hidden in v1** (mini
  bar has no bookmark control until v1.1 lands).

Mini bar height is unchanged from today.

## Now Listening overlay

Full-viewport overlay, dark surface (`bg-background`). Z-index above
the mini bar, below modals.

Layout on desktop (width ≥ md):

```
┌─────────────────────────────────────────────────────────────────┐
│ ⌄ (collapse, top-left)                       ⋯ More (top-right) │
│                                                                  │
│            ┌──────────────────────┐                              │
│            │                      │   PROJECT HAIL MARY          │
│            │      [ COVER ]       │   Andy Weir                  │
│            │      ~360 × 540      │   Narrated by Ray Porter     │
│            │                      │                              │
│            │                      │   ── CHAPTER 7 ──            │
│            │                      │   The Astrophage             │
│            └──────────────────────┘                              │
│                                                                  │
│   [ tall SeekBar with chapter ticks ]                            │
│   12:43                                              -4:08:35    │
│                                                                  │
│              ◀30      ▶❚❚ (large)      30▶                       │
│                                                                  │
│   ⌛ Sleep   ☰ Chapters (24)   1× Speed                          │
└─────────────────────────────────────────────────────────────────┘
```

Layout on mobile (portrait): same elements stacked top-to-bottom —
header row, cover (centered, smaller), metadata block, seek bar,
transport, utility row. No horizontal split.

Key behaviors:

- **Tap right time** toggles between `-remaining` and the wall-clock
  end time. The toggle preference is in-memory only (resets per
  session) — no persistence concern.
- **Sleep timer** opens `SleepTimerMenu`. When armed, the trigger
  shows the countdown (`Sleep 04:32`). When the timer hits zero the
  player fades volume to 0 over 5s and pauses.
- **Car mode** and **bookmark** buttons are **hidden in v1** — the
  utility row ships with sleep / chapters / speed only. Car mode +
  bookmark slots are documented here so v1.1 has a clear home for
  them.
- **Overflow ⋯** menu items for v1: "Go to detail page", "Report a
  sync problem" (opens a mailto/issue link). Future additions live
  here.

The same `<audio>` element backs both modes, so all controls in Now
Listening are bound to the same `useAudiobookPlayback` state.

## Detail page restructure

The page keeps its current top-level shape (hero band + content
below) and adds/rearranges these blocks:

### Hero band

Today: `DetailHero` with a stack of `Play` / `Play from Start`
actions and a small progress bar that appears below the buttons only
when progress exists.

New version (still using `DetailHero` but with a richer `actions`
slot):

```
[ progress bar — full width of actions column, always shown if any progress ]
[ "3h 12m listened · 19%" caption ]

▶ Resume Ch 7 · The Astrophage     ↻ Start Over     🔖 Bookmarks (3)
```

- Progress bar moves up *above* the buttons so the listener sees
  position context first.
- Resume button label includes the chapter the listener will land
  in. Pulled from the same `buildChapterList` already in the file.
- "Bookmarks (n)" button visible only when n > 0; opens a side sheet
  listing them. v1 ships with an empty stub (always 0, button never
  shown) — full feature in v1.1.

### Chapters section

Today: collapsed-by-default disclosure showing a chapter list.

New version:

- Expanded by default. (Collapsed in this position made sense when
  the page was sparse; once we're foregrounding chapter awareness
  everywhere else, hiding the list here is contradictory.)
- Currently-playing chapter row gets a left-edge accent and a
  "▶ playing" badge on the right.
- Lightweight sort menu in the section header: "By position" (default),
  "Longest first" — no UI for "By title" since chapter titles rarely
  sort meaningfully. Sort preference is in-memory only for v1.
- Clicking a chapter row still calls `openPlayer(absoluteStart)` as
  today; if the player is open, it seeks rather than reopens.

### Narrator card

New section between Chapters and the cross-rails. Renders only when
`data.narrator` is non-empty.

```
── NARRATOR ──
[ avatar 64×64 ]  Ray Porter
                   47 audiobooks in your library  →
```

Avatar source: if the absorbed audiobook data model exposes a
narrator photo, use it; otherwise an initials avatar in a circle.
The `→` link goes to a narrator detail page if one exists, or a
search-results page filtered by narrator name as a fallback.

### Cross-book rails

Three sections, each rendered only when its data is present, ordered
top-to-bottom:

- **"Similar audiobooks"** — embedding-based recommendations. Ranked
  by vector similarity against this book; ordering and inclusion
  decided server-side. Renders with a subtitle "Based on listening
  patterns" to give the rail a small bit of provenance copy that the
  metadata rails don't need.
- **"Also by {author}"** — horizontal scroller of cover tiles, same
  pattern as `MediaRow` used elsewhere in Silo.
- **"In this series"** — same pattern, with "Book n of m" subtitle and
  the current book non-clickable / highlighted.

All three rails ship behind feature-detection: if the API response
for the book includes the related array, render it; if not, hide the
section without showing a placeholder. This avoids blocking v1 on any
specific backend rail support — each lights up as the data lands.

### What's *not* in the new detail page

- Reviews / ratings — out of scope, not in the data model.
- Tabs — page is short enough that one scroll surface beats tabs.
- Social / cohost listening — no signal it's wanted.

## Data & API impact

Most of the work is pure frontend, but three small backend-adjacent
items to call out:

| Item | Impact |
|---|---|
| Sleep timer | Frontend-only. No API change. |
| Speed menu | Frontend-only — already a client-side audio setting. |
| Bookmarks | **Deferred to v1.1.** Needs `audiobook_bookmarks` table (`content_id`, `profile_id`, `position_seconds`, `note`, `created_at`) plus CRUD endpoints. v1 ships with the button hidden and `useAudiobookPlayback` exposing no-op bookmark stubs. |
| Narrator card with "n audiobooks in library" count | Needs narrator-aware query. If the detail endpoint already returns it, render; otherwise hide the count line and show only the name. |
| "Also by author" / "In this series" rails | Render only when arrays present. v1 doesn't require backend changes — it just adapts to whatever the response contains. |
| "Similar audiobooks" rail (embedding-based) | Render only when `similar_audiobooks` array present on the detail response. Backend computes similarity from per-book embeddings server-side and includes the top-N already ranked. v1 frontend has no embedding logic — it just renders whatever ordered list the API returns. |

## Routing & view-transition wiring

- `/audiobooks/book/:contentId` continues to be the only audiobook
  route added by this work. Mini bar and Now Listening are layered
  inside this route's component tree.
- View transition name: `audiobook-cover-{contentId}`. Set on the
  cover tile inside `CoverExpandTile` and on the large cover element
  inside `NowListening`. Same `contentId` on both = the browser
  animates the rect change automatically.
- The detail page's hero cover and the mini bar's cover tile share
  the same view-transition name so the *initial* open (clicking
  Play/Resume on the hero) also animates the cover into the mini bar
  position.

## Accessibility

- All transport controls have `aria-label`s (already true today; keep
  parity).
- The expand chevron and the cover tile are both labeled "Expand
  player" / "Open Now Listening" — two ways in, both discoverable to
  screen readers.
- `SleepTimerMenu`, `SpeedMenu`, `ChaptersMenu` use the existing
  `Popover` primitive with proper roving focus + Escape-to-close
  (inherits from `ChaptersMenu` behavior).
- Now Listening overlay traps focus while open and restores focus to
  the mini bar's expand chevron on close.
- Color contrast for the chapter-title text in the mini bar must
  meet WCAG AA against the bar's background (use `text-foreground`
  with a `text-muted-foreground` chapter title is the existing
  pattern and passes).

## Testing

- **Unit:** `useAudiobookPlayback` — chapter computation from current
  time across multi-file audiobooks, sleep-timer arm/disarm/fire,
  progress reporting cadence and pause/seek/end triggers.
- **Component:** `MiniBar` and `NowListening` render expected
  elements given a mocked playback object; expand/collapse calls the
  right callbacks; speed menu / sleep menu open and emit the right
  values.
- **Integration / Playwright** (if the repo has Playwright wired for
  these flows): start playback from detail page → mini bar appears
  with chapter title → click cover tile → Now Listening overlay
  appears → seek bar works in both modes → collapse returns to mini
  bar with playback uninterrupted (key assertion: `audio.currentTime`
  monotonically advances across the mode swap).

## Out of scope (recap)

- Bookmarks backend (v1.1 sub-spec)
- Cross-book rails backend joins
- Car mode full layout
- Audiobook library grid redesign
- Lifting the player to an app-level provider so it survives
  navigation away from the detail page

## Open questions / risks

- **Multi-file audiobooks.** Today's player only plays `files[0]` —
  multi-file audiobooks display all chapters but can't actually cross
  file boundaries. This redesign doesn't fix that; it inherits the
  limitation. Worth flagging in the implementation plan whether to
  fold a fix into v1 or leave it for a separate ticket.
- **View transition browser support.** CSS view transitions are
  supported in modern Chromium and Safari Tech Preview; Firefox is
  not there yet. The mini ↔ Now Listening swap must work without
  the transition (graceful fallback to a plain unmount/mount).
- **Sleep-timer fade.** A 5s gain ramp on `HTMLAudioElement.volume`
  works but is not as smooth as a Web Audio API gain node. Starting
  simple; revisit if it feels janky.
