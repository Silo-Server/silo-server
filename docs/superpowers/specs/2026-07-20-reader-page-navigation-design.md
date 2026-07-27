# Ebook Reader Page Navigation Redesign

Date: 2026-07-20
Status: approved (visual mockup review)

First of four planned reader improvements (navigation → reading stats →
dictionary/translate → PDF engine). This spec covers navigation only.

## Problem

The prose reader's navigation chrome is minimal to a fault. A bare fraction
slider in the header says "34%" with no sense of position within the chapter —
the orientation readers actually feel. There is no touch navigation (edge
buttons only), chrome is always visible and eats vertical space on phones,
and keyboard support stops at arrow keys with nothing discoverable.

## Design

Modeled on BookOrbit's footer, adapted for silo's touch usage. Applies to
prose formats only; comics/manga keep their existing dedicated navigation.

### Footer bar (replaces the header slider)

- Progress slider spanning the whole book, with the **current chapter's
  extent rendered as a highlighted band** under the thumb. Dragging scrubs
  the whole book; the band recomputes on chapter change and shows the
  chapter title as a tooltip.
- Left slot: current chapter title. Books without a TOC hide the band and
  title and degrade to the plain slider.
- Right slot: percentage, plus an estimated time-remaining once the
  reading-stats feature (next sub-project) provides session/pace data. Until
  then the slot shows percentage only — no placeholder.
- A `?` affordance opens the keyboard-shortcuts overlay.

### Tap zones and chrome auto-hide

- The page area splits into three tap regions: left third pages back, right
  third pages forward, middle third toggles chrome visibility.
- Hidden chrome means both header and footer; the page fills the viewport.
  Chrome reappears on middle tap, `Esc`, or mouse movement to the top/bottom
  viewport edge (pointer devices).
- In scrolled flow, left/right tap zones are disabled (taps would fight text
  selection and scrolling); middle-tap chrome toggle still applies.
- Text selection within a tap zone must win over paging: a tap that ends a
  selection gesture does not turn the page.

### Keyboard

- Existing: `←`/`→` page turns.
- New: `Home`/`End` jump to current chapter start/end, `t` TOC panel,
  `s` search panel, `b` toggle bookmark, `f` fullscreen, `?` shortcuts
  overlay, `Esc` closes overlay/panels or toggles chrome.
- The shortcuts overlay is a dismissable modal listing the map; it must not
  capture keys while a text input (search, notes) has focus — none of the
  single-letter shortcuts fire from inputs.

### Header (unchanged tools, less clutter)

Bookmark, highlight, ruler, TTS, settings, and panel toggles stay in the
header. The slider leaves it, which is the only header change.

## Implementation shape

Client-only — no API or schema changes.

- `web/src/reader/ReaderFooter.tsx` (new): slider + chapter band + labels +
  shortcuts affordance. Pure presentational; receives fraction, chapter
  metadata, and callbacks.
- Chapter extent: derived from foliate's section sizes and the current
  section index on `relocate` events (the loader already exposes section
  fractions); computed in a small pure helper so it is unit-testable.
- Tap zones: a pointer-event layer in the reader page container
  (`web/src/pages/EbookReader.tsx`), gated on flow mode and on selection
  state; unit-tested as a pure decision helper (given pointer event +
  selection + flow → action).
- Chrome visibility: single `chromeVisible` state driving header/footer
  render; auto-hide interactions listed above.
- Shortcuts overlay: `web/src/reader/ReaderShortcutsOverlay.tsx` (new),
  static content, focus-trapped.
- Keyboard handling: extend the existing key handler in `EbookReader.tsx`
  with the input-focus guard.

## Error handling

- No TOC / single-section books: band and chapter title hidden.
- Fraction-only formats (position without CFI): slider and band operate on
  fractions exactly as today's slider does.
- Time-remaining renders only when pace data exists (feature-detected from
  the stats API once built); no layout shift when absent.

## Testing

- Pure helpers (chapter extent, tap-zone decision, shortcut dispatch) get
  focused vitest suites written first.
- `EbookReader.test.tsx` extended: footer renders chapter metadata, chrome
  toggling, input-focus guard, comics unaffected.
- Manual pass on phone-sized viewport for tap zones and auto-hide.

## Out of scope

- Reading stats and the time-remaining data source (next sub-project).
- Printed page numbers / go-to-page (deferred; revisit if EPUB page-list
  demand appears).
- Comics/manga navigation, PDF navigation (PDF engine is a later
  sub-project).
- Any server-side change.
