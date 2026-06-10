# Audiobook Player QoL Improvements

Commands assume the repository root is the cwd. All paths are repository-relative.

## Goal

Bring the web audiobook player to feature parity with best-in-class audiobook apps
(Audiobookshelf, Audible, BookPlayer, Prologue) on the quality-of-life axis. The current
surface architecture (persistent mini bar + expandable Now Listening view, playback
survives navigation) is already the de-facto standard and stays as-is.

Explicitly **out of scope**: Media Session / lock-screen integration, bookmarks (needs a
server endpoint — follow-up), offline/download, queue.

## Features

1. **Keyboard shortcuts** — space/K play-pause, arrow skips, volume, chapter nav, speed step.
2. **Configurable skip intervals** — asymmetric defaults (back 10s, forward 30s), user-tunable.
3. **Expanded speed control** — 0.5×–3.0× with fine stepping, presets, and per-book memory.
4. **Chapter prev/next buttons** — alongside the seconds-skip buttons.
5. **Volume control** — reuse the shared `VolumeControl`, persisted like the video player.
6. **Smart rewind** — auto-rewind on resume, scaled by how long playback was paused.
7. **Time remaining at current speed** — real-clock remaining display in Now Listening.

No server/API changes are required; everything is web-frontend only, so no Android/Apple
client coordination is needed. (Per-book speed memory is device-local in v1; syncing it
server-side via profile settings is a noted follow-up.)

## Existing seams (read these first)

| File | Relevance |
| --- | --- |
| `web/src/pages/audiobooks/player/useAudiobookPlayback.ts` | Playback engine. `togglePlay` (~line 499), `skip` (~534), `setRate` (~541), returned API (~690+). All engine work lands here. |
| `web/src/pages/audiobooks/player/MiniBar.tsx` | Bottom bar. Hardcoded `SKIP_*_SECONDS = 30` and `PLAYBACK_RATES` at lines 10–12. |
| `web/src/pages/audiobooks/player/NowListening.tsx` | Full-screen view. Same hardcoded constants; dead "More" button (lines 50–56) becomes the settings menu trigger; remaining-time toggle at lines 33–37. |
| `web/src/pages/audiobooks/player/AudiobookPlayer.tsx` | Wrapper that owns mini/now-listening mode — mount point for the keyboard shortcuts hook. |
| `web/src/player/components/VolumeControl.tsx` | Existing slider + `getPersistedVolume`/`persistVolume` helpers (shared `player-volume`/`player-muted` storage keys). Styled for dark video overlay — needs a surface-toned variant. |
| `web/src/player/components/SpeedMenu.tsx` | Current preset-only menu (shared with video player). Audiobook gets a richer `SpeedControl`; video keeps `SpeedMenu` untouched. |
| `web/src/player/hooks/useKeyboardShortcuts.ts` | Video player's shortcut hook — pattern to mirror (input-field guard, single document listener). |
| `web/src/utils/storage.ts` | Typed localStorage wrapper — add new keys here, never call `localStorage` directly. |
| `web/src/player/components/SeekBar.tsx`, `CircleButton.tsx`, `SleepTimerMenu.tsx`, `ChaptersMenu.tsx` | Established player design language; reuse, don't fork. |

## Design language

These are additions to an existing, refined system — extend it, don't restyle it:

- **Surfaces**: mini bar is a themed surface (`bg-background`, `border-t`, token colors).
  Popovers anchored to it use the established dark-glass treatment
  (`bg-black/90 backdrop-blur-sm rounded-lg shadow-xl`, white/75 text, `data-active` rows)
  exactly as `SpeedMenu`/`SleepTimerMenu` do today.
- **Type**: time and rate values are always `font-mono`/`tabular-nums` so nothing shifts as
  digits tick. The new speed readout (`1.45×`) must reserve width for two decimals.
- **Buttons**: all transport controls are `CircleButton` (`sm` secondary flanks, `md`/`lg`
  primary center). Chapter prev/next use `SkipBack`/`SkipForward` lucide glyphs at the same
  stroke weight (1.6) as the rotate icons, so the cluster reads as one family.
- **Micro-interactions**, small and purposeful:
  - Skip buttons: a quick ~18° icon flick in the skip direction on press
    (CSS transform transition, ~150ms ease-out) — confirms the action without a toast.
  - Speed stepper: value crossfades (opacity 120ms) on change; the active preset chip gets
    the same `bg-white/5 text-white` treatment as current menu rows.
  - Smart rewind: when it fires, the seek bar playhead animates the small jump back rather
    than teleporting (SeekBar already animates position; verify it holds for ≤30s deltas).
- **Accessibility**: every new control keeps the existing patterns — `aria-label`,
  `aria-expanded`/`aria-haspopup` on triggers, roving focus + Escape in menus (copy from
  `SleepTimerMenu`), `role="slider"` semantics from `VolumeControl`/`SeekBar`.

## Phase 1 — Engine + preferences model

### 1a. Preferences hook

New `web/src/pages/audiobooks/player/useAudiobookPrefs.ts`:

- Add storage keys: `AUDIOBOOK_SKIP_BACK`, `AUDIOBOOK_SKIP_FORWARD`,
  `AUDIOBOOK_SMART_REWIND` (bool), `AUDIOBOOK_RATES` (JSON map) in `web/src/utils/storage.ts`.
- Exposes `{ skipBack, skipForward, smartRewind, setSkipBack, setSkipForward, setSmartRewind }`.
- Defaults: back **10s**, forward **30s** (the asymmetric convention — small "what did she
  say?" hops back, bigger hops forward), smart rewind **on**.
- Allowed skip values: 5, 10, 15, 30, 45, 60, 90 (two digits max keeps `SkipIcon`'s inset
  number legible).
- Per-book rate map: single JSON object `{ [contentId]: rate }` under `AUDIOBOOK_RATES`,
  LRU-capped at 50 entries (store `{ rate, at }` and evict oldest on insert).
  Helpers `getBookRate(contentId)` / `setBookRate(contentId, rate)` live here too.

### 1b. Engine extensions (`useAudiobookPlayback.ts`)

- **Volume/mute**: initialize from `getPersistedVolume()`; apply to the `<audio>` element in
  an effect; expose `volume`, `muted`, `setVolume`, `setMuted`; persist via `persistVolume`.
  Sharing the video player's keys is intentional — one volume preference per device.
- **Per-book rate**: on mount, initialize `rate` from `getBookRate(contentId)` (fallback 1);
  in `setRate`, also `setBookRate`. The hook needs `contentId` passed in (it already
  receives the files/options object — extend that input).
- **Chapter navigation**: expose `nextChapter()` and `prevChapter()`.
  - `next`: seek to start of the chapter after `currentChapter`; no-op at the last chapter.
  - `prev`: if more than 3s into the current chapter, seek to its start; otherwise seek to
    the previous chapter's start (the universal music/audiobook convention).
- **Smart rewind**: record `pausedAtRef = performance.now()` whenever playback pauses (in
  the `pause` event handler so it also catches OS-initiated pauses). On resume, before
  `audio.play()`, compute rewind from pause duration and `seekTo(currentTime - rewind)`:
  - < 10s paused → 0s; < 1min → 3s; < 10min → 10s; < 1h → 20s; ≥ 1h → 30s.
  - Clamp at 0; skip entirely when the smart-rewind pref is off or the resume immediately
    follows a user seek (seeking sets a short suppress flag so explicit jumps aren't undone).
  - Also apply on **cold resume**: when starting with `initialPositionSeconds > 0` from the
    detail page, rewind by the same schedule using the age of the saved progress if the
    API exposes an updated-at timestamp; otherwise apply a flat 10s. Keep this logic in one
    pure function `smartRewindSeconds(pauseMs)` so it's unit-testable.
- **Skip intervals**: no engine change — `skip(delta)` already takes a signed delta; the UI
  passes the configured values.

### 1c. Tests

Vitest, colocated like existing `*.test.tsx`:

- `smartRewindSeconds` schedule boundaries.
- Per-book rate LRU (cap, eviction, malformed-JSON tolerance).
- `prevChapter` 3s threshold behavior and first/last chapter edges
  (chapter math is pure — test against `buildChapterList` fixtures from
  `web/src/lib/audiobooks/chapters.ts`).

## Phase 2 — Controls UI

### 2a. SpeedControl (new, audiobook-specific)

New `web/src/pages/audiobooks/player/SpeedControl.tsx`, replacing `SpeedMenu` usage in both
audiobook views (video player keeps `SpeedMenu`):

- Trigger: same `player-utility-btn` showing the current rate (`1.45×`, tabular-nums).
- Popover (dark-glass, bottom-anchored like today):
  - A stepper row: `−` / big mono readout / `+`, stepping **0.05** per tap, clamped to
    **0.5–3.0**. Press-and-hold repeats (250ms initial, 80ms repeat).
  - A preset chip row: 1× · 1.25× · 1.5× · 1.75× · 2× · 2.5× · 3×.
  - Footnote line: "Remembered for this book" (quiet `text-white/40`, 11px) — makes the
    per-book memory discoverable instead of magical.
- Keyboard: ArrowUp/Down step inside the popover; Escape closes (copy menu plumbing from
  `SleepTimerMenu`).

### 2b. Chapter prev/next buttons

- Add `SkipBack`/`SkipForward` `CircleButton size="sm" variant="secondary"` on the outside
  of the transport cluster in both `MiniBar` and `NowListening`:
  `⏮ ↺10 ▶ ↻30 ⏭`. Wire to `prevChapter`/`nextChapter`; render only when
  `playback.chapters.length > 0`; disable at the ends (prev stays enabled mid-chapter).
- In `MiniBar`, hide the chapter buttons below `sm:` — the 3-column grid is already tight
  on phones and the chapters menu remains available.

### 2c. Volume

- Add a `tone` prop to `web/src/player/components/VolumeControl.tsx`
  (`"overlay"` = current white-on-dark, `"surface"` = theme tokens: track `bg-muted`,
  fill `bg-foreground`, focus ring `ring-ring`). Default `"overlay"` so the video player is
  untouched.
- Mount in `MiniBar`'s right cluster (before the sleep timer) at `md:` and up, and in
  `NowListening`'s utility row. Wire to the new engine `volume`/`muted` state.

### 2d. Player settings menu

New `web/src/pages/audiobooks/player/PlayerSettingsMenu.tsx` (dark-glass popover):

- **Skip back** / **Skip forward**: rows of small value chips (5/10/15/30/45/60/90s).
- **Smart rewind**: toggle row with a one-line description ("Backs up a little after a pause").
- Triggers: the currently-dead "More" (`MoreHorizontal`) button in `NowListening`
  (lines 50–56) and a matching trigger in `MiniBar`'s right cluster.
- `MiniBar`/`NowListening` read `useAudiobookPrefs()` and pass the configured seconds to
  the skip buttons and `SkipIcon` (which already renders a dynamic number) — delete the
  `SKIP_*_SECONDS` constants.

## Phase 3 — Shortcuts + time display + polish

### 3a. Keyboard shortcuts

New `web/src/pages/audiobooks/player/useAudiobookKeyboardShortcuts.ts`, mounted in
`AudiobookPlayer.tsx` (so it's active in both mini and expanded modes, on every route):

| Key | Action |
| --- | --- |
| Space / K | play-pause |
| ← / → | skip by configured back/forward seconds |
| ↑ / ↓ | volume ±5% |
| M | mute toggle |
| N / P | next / previous chapter |
| Shift+. / Shift+, | speed +0.05 / −0.05 (YouTube convention) |
| E | expand / collapse Now Listening |
| Esc | collapse Now Listening (expanded mode only) |

- Mirror `useKeyboardShortcuts.ts` guards: ignore when target is input/textarea/
  contentEditable, single document listener, cleanup on unmount.
- Additional guard: **do nothing while a video session is active** — the video player binds
  the same keys. Check `WatchPlaybackProvider` state (or simply whether a `<video>` watch
  route is mounted) and bail; the audiobook bar is backgrounded in that case anyway. Space
  must also not fire when a button has focus (let the focused control handle Enter/Space).
- Add `title` tooltips with the shortcut hint to transport buttons
  (e.g. `Back 10 seconds (←)`) for discoverability.

### 3b. Time remaining at current speed

In `NowListening`, the right time label currently toggles total ↔ remaining. Make it cycle
three states (persist last choice in component state only):

1. total (`12:04:00`)
2. remaining (`−3:21:09`)
3. remaining at speed (`−2:14:06 at 1.5×`) — `remaining / rate`, hidden (skipped in the
   cycle) when `rate === 1`.

Keep the `data-testid="now-listening-right-time"` hook and update its test.

### 3c. Polish pass

- Skip-flick and speed-crossfade micro-interactions from the design section.
- Verify mini bar layout on 360px-wide viewports with all new controls (chapter buttons and
  volume hidden, settings menu accessible).
- `cd web && pnpm run lint && pnpm run format:check`; run the player test suites.

## Risks / notes

- `useAudiobookPlayback.ts` is 707 lines and growing — extract smart rewind + prefs-coupled
  logic into small pure modules (`smartRewind.ts`) rather than inflating the hook; consider
  splitting volume/rate concerns into a `useAudioElementSettings` helper if the hook passes
  ~800 lines.
- Smart rewind must not fight the 10s progress reporter: rewinding on resume changes
  `currentTime`, which triggers a progress write — that's correct (the rewound position is
  the truth), just confirm no oscillation with the suppress-after-seek flag.
- Per-book rate uses `contentId`; switching narrators navigates to a different `contentId`,
  so each narration remembers its own speed — acceptable, arguably desirable.
- Cross-device sync of prefs/per-book speed is a candidate follow-up once a profile
  settings endpoint exists; the `useAudiobookPrefs` API is the seam to swap storage behind.
