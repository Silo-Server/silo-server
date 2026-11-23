# Translating the web app

Interface text lives in the shared catalog in `Silo-Server/silo-translations`
(`catalog/en.arb`), not in this repository. `scripts/sync-translations` generates
`web/src/locales/` from it: one precompiled message file per language
(`<tag>.json`), and `keys.ts` with the `MessageKey` type of every valid id.
Commands below assume the repository root is the cwd.

## Converting an area

1. Add the English strings to `catalog/en.arb` in silo-translations, under
   `web.<area>.<thing>` (lowerCamel segments) with `"x-platforms": ["web"]`. Reuse a
   `common.*` key only when its English is byte-identical to what the screen shows
   today: English must not change during extraction. Every key needs a `description`
   that says where the text appears and what each placeholder holds. Run `make check`.
2. Sync: `scripts/sync-translations --local <silo-translations checkout>` (or
   `--version <release>` once releases exist). Commit `web/src/locales/` and
   `.silo-translations` with the change. Never edit generated files.
3. Replace the literals (below). ESLint (`I18N_CONVERTED` in `web/eslint.config.js`
   covers all of `src/` except `src/i18n`, generated locales and tests) rejects literal
   JSX text, literal text in visible props (`label`, `title`, `description`, `help`,
   `placeholder`, `unit`, …, `aria-*`) and literal toast messages. It can't see strings
   a plain helper returns, object-literal labels, units built in template literals or
   lists joined with `join(", ")` (use `joinList()` from `@/lib/appLocale`); the
   pseudo-locale pass below does.
4. Check the area under the pseudo-locale (below). Anything still plain English is
   unconverted.

## The API

There is no `defaultMessage` in code: English comes from the bundled `en.json`, so
there is one source of English. An unknown id fails the typecheck.

```tsx
import { useT } from "@/i18n";

function Dialog({ row }: { row: HomeRow }) {
  const t = useT();
  return (
    <DialogTitle>{t("web.home.rows.addToOtherLibraries.title", { row: row.title })}</DialogTitle>
  );
}
```

- **Components** call `useT()` (and `useServerText()` for server text). They re-render
  when the language changes. ESLint stops `.tsx` files importing `t`/`serverText`
  directly, and every file importing `react-intl`.
- **Plain code** (lib helpers, error classes) calls `t(id, values)`. Call it when the
  text is used, never at module scope. A component that shows text from such a helper
  calls `useAppLanguage()` so it re-renders on a language change.
- **Plurals:** one `plural` spanning the message,
  `{count, plural, one {# page} other {# pages}}`; pass `count` as a number. A
  language without the message falls back to English with English plural rules.
  Wording that refers to the items as one or several ("it" / "them") selects on
  `{amount, select, exactlyOne {…} exactlyTwo {…} other {…}}` instead; pass
  `amount: exactAmount(count)` (see docs/architecture/i18n.md).
- **Placeholders:** pass dates, durations and sizes already formatted, as strings
  (`formatLocale()` / `appLanguage()` from `@/lib/appLocale`).
- **Digits:** in Arabic, numbers print in the format locale's digits, as dates do
  (Arabic-Indic on ar-EG, Latin on ar-AE): the plural `#` and number values. A
  string is never changed, so pass a number that is shown as a number, not
  `String(n)`; a plain `{n}` prints it without grouping in every language. A number
  shown outside a message, such as a year, goes through `numberText()`; a value
  that is sent, matched or put in a URL never does. A season or episode number
  inside a code ("S01E05", "E5") goes through `codeNumber()` and stays Latin in
  every language; "Season 2" in words passes the number.
- **Apostrophes:** the catalog validator rejects an ASCII `'` next to a brace or `#`
  (`{name}'s`, `'{title}'`). Extraction can't change English, so such a string stays
  literal, with an `eslint-disable` comment saying why, until a wording change replaces it.
- **Data stays English:** text that is stored, sent to the server, matched or
  compared (a saved track signature's label, a default note written to the
  server, a value another client reads) is data, not interface text. Keep it
  English and translate only the label that is shown; split the two when one
  helper builds both. Code that found an element by its English label finds
  it by a data attribute instead.
- **Right to left:** in an RTL language every printed string placeholder is
  wrapped in Unicode isolates, so a left-to-right title keeps its punctuation in
  place. `select` and `plural` arguments are not, and neither is a placeholder
  printed only as the whole content of a `code`, `url`, `address`, `mono` or `id`
  tag (`<code>{command}</code>`), so it copies without invisible characters.
  Render those tags as `<code>` elements, which app.css isolates (add
  `[font-family:inherit]` for text that isn't monospace). English bytes never change.
- **Web-only keys** may use `select` and rich-text tags:
  `"Read the <link>guide</link>"` with
  `t(id, { link: (chunks) => <a href={url}>{chunks}</a> })`, which returns React nodes.
  Shared keys (no `x-platforms`) can't.
- **Server text** (problem details, section titles): `useServerText()(key, args, text)`
  shows the translation only when the interface language's catalog has `key` and
  `args` fill it; otherwise, and always in English, the server's text as sent.
  `V2ProblemError.message` does this with the first field error's
  `error.field.<code>`, then `error.<type>`, in the language current when the error
  arrived; `problemFieldDetail()` for one field error; `useSectionTitle()` for section
  titles.

## Languages and pseudo-locales

- A language's translations ship when the sync generates its `<tag>.json`; `LOCALES` in
  the generated `keys.ts` lists it then, and `hasCatalog` in `src/lib/appLocale.ts`
  reads it. Only these languages follow the browser's language and appear in the pickers;
  a profile's stored choice applies any language in `SHIPPED_LANGUAGES`. For a language
  listed there, shipping takes only the regenerate; a new language also needs its row there
  and its name data in `NAME_DATA` (`src/lib/languageNames.ts`).
- Pseudo-locales are bundled only in `vite dev` and builds with
  `VITE_PSEUDO_LOCALES=1`. There, the pickers offer them and a stored `en-XA`
  (accented, longer) or `ar-XB` (right to left) applies; a sandbox can also set the
  profile's `ui.app_language` to one through the settings API.

## Right to left

`<html dir>` follows the applied language, and `LocaleProvider` passes it to Radix
(`Direction.Provider`). Code that needs it calls Radix's `Direction.useDirection()`
(Embla carousels, the calendar, the toaster, scroll strips) or reads the element's
computed direction (`isRightToLeft` and `readingArrowKey` in `src/lib/direction.ts`).

- Layout uses logical utilities: `ps-`/`pe-`, `ms-`/`me-`, `start-`/`end-`,
  `text-start`/`text-end`, `rounded-s`/`rounded-e`, `border-s`/`border-e`, and logical
  CSS properties. ESLint (`eslint-rules/no-physical-direction.js`) flags physical ones
  and `--fix` converts them. `cn()` merges them the way it merges physical ones.
- Direction is physical on purpose for the media timeline and transport, chart plots,
  and anything placed on poster artwork (overlay badges, card corners), and for
  `env(safe-area-inset-*)` edges. Those files are in `PHYSICAL_DIRECTION` in
  `web/eslint.config.js`; a single line takes an `eslint-disable-next-line` with its
  reason; a `dir="ltr"` box keeps flex order and text there left to right. `pre` is
  always left to right; inline `code`, `kbd` and `samp` take their text's direction
  unless they carry a `dir`.
- An input or read-only field that holds a URL, server path, or network address
  (hostname, IP address, CIDR list) sets `dir="ltr"` (`SettingField` takes a `dir`
  prop); otherwise trailing `/`, `.` or `…` moves to the start, and a comma-separated
  list draws in reverse order. When the field can show a translated placeholder or
  message instead, set it only while it shows the value
  (`dir={value ? "ltr" : undefined}`).
  `dir="auto"` doesn't do this: an empty field resolves to left to right, so a
  translated placeholder lands on the wrong side. `type="url"` rejects addresses
  without a scheme.
- A path or URL shown as text (a `code` block, a list of folders) also sets `dir="ltr"`,
  and the block that holds it takes `align-with-page` (in `app.css`). The class gives the
  block its parent's `text-align`, with `start` and `end` resolved in the parent's
  direction, so under a start-aligned parent that runs in the page's direction every line,
  wrapped or not, sits on the page's start side. Without it, an inherited `start` resolves
  against the block's own direction and puts every line at its left edge. Chromium shows
  that wherever it happens; Firefox and Safari give `li` `text-align: match-parent`, so a
  list item in a start-aligned right-to-left list, even `<li dir="ltr">`, and everything
  inside it are right-aligned there anyway. A folder name that can't wrap, such as one
  long emoji sequence, still runs past the block's right edge, the page's start side in a
  right-to-left page: clip it sideways while it overflows, with `useOverflowsInline` and
  `OVERFLOW_CLIP` (`src/hooks/useOverflowsInline.ts`), so it keeps its start and stays in
  the block. `PathList` (`src/components/PathList.tsx`) and `CardPath` handle the
  alignment and the clip. Don't give a list of paths `w-fit` instead: one such name would
  make the list that wide, and its other paths would wrap at that width.
- A name that can be in either script and is cut with `truncate` (a username, say) takes
  `dir="auto"`: it reads in the direction of its first strong character (a letter, or
  right-to-left punctuation such as `؟`), and left to right when it has none, as a name
  made only of digits does. A name in one script then keeps its start. In a name that
  mixes directions, a part that runs the other way, such as a long run of digits before
  Arabic letters, can still lose its own start; no `dir` avoids that for every name.
  Where the box would stretch (a block, a `flex-col` child), size it to its text with
  `w-fit max-w-full` so a short name stays on the page's start side. A `flex-1` item
  can't be sized that way: keep `flex-1` on a wrapper and put the name in a
  `block w-fit max-w-full` span inside it. A `flex-1` item capped at its text with
  `max-w-max` can't stretch, so it can take `dir="auto"` itself unless it also carries
  a logical margin, padding or border (`ms-auto`, `pe-2`, `border-s`): those follow
  the element's own direction, so keep them on the outer element and put the name in
  a `block` span inside it. A name that can also wrap takes `align-with-page`, so its
  wrapped lines stay on the page's start side in every browser; Chromium otherwise puts
  them on the name's own start side. A clip, or a measurement of whether the name
  overflows, goes on the name's own box, since it overflows toward its own end.
  `.bidi-text` alone doesn't keep the start: its `unicode-bidi: plaintext` leaves the box
  in the page's direction, so Firefox drops the "…" and WebKit cuts the start.
- A name inside a longer line (a name and "(you)", a sentence that names someone, a
  name and a device) takes `dir="auto"` on its own element, never on the line, where
  the name's script would set the direction of the translated words too. Pass that
  element to `t()` as the value
  (`t(id, { name: <span dir="auto" className="truncate">{name}</span> })`). When the
  words must stay and the name is cut, make the line a
  `flex overflow-hidden whitespace-pre` box: the words become flex items that keep the
  page's direction and don't shrink, and `whitespace-pre` keeps the spaces at their
  edges. User text on the line (a note, a device name) is its own `dir="auto"`
  `truncate` element too, whose `nowrap` folds any line breaks `pre` would keep. A
  rich-text tag that wraps the name (`<strong>{name}</strong>`) is the flex
  item, so it needs `overflow-hidden` too, or the name can't shrink inside it. Where
  the row can squeeze the line narrower than its words (the impersonation banner),
  the words shrink too, but only once the name is down to a letter and "…"
  (`wordsYieldLast` in `ImpersonationBanner.tsx`); where the row has other text that
  doesn't shrink, cap it (`max-w-1/2 truncate`) so the line keeps room. When the text
  after the name is cut first (a suggestion's note, a requester's device), give the
  name `max-w-full shrink-0`: the name is then cut only when it alone fills the line
  (`SuggestionList.tsx`).
- Titles, names, taglines and overviews from metadata use `.bidi-text` (`app.css`).
  Where such text is cut with an ellipsis (`truncate`, `line-clamp`, or a clamp in a
  stylesheet), it also takes `dir="auto"`. Without it, text that runs the other way
  loses its "…" in Firefox and its start in WebKit, and a clamped paragraph gets its "…"
  at the start of its last line. `.bidi-text` keeps a short one on the page's start
  side, so it needs no `w-fit`. The mixed-direction limit above applies here too.
  Clamped text also sets `data-clamp-cut` on the clamped `.bidi-text` element while the
  clamp cuts it, or Chrome drops its "…" (`app.css`). Take it from `useClampCut`
  (`src/hooks/useClampCut.ts`), or from a measurement the component already
  makes, as `DetailOverview` and `DetailTitle` do.
- A cut title inside a line of translated words ("S01E02 · Title", "Play Title")
  goes to `t()` as a `LineTitle` and the line renders through `TitleLineParts`
  (`src/components/TitleLine.tsx`), in a `flex flex-wrap overflow-hidden` box (no
  `flex-wrap` in a pill or a button). The title is cut in its own box and direction;
  `dir="auto"` on the whole line would let the title set the words' direction too.
  A list of names (the crew list) stays in the page's direction and gives each name
  `dir="auto"`, so the names keep their order and the list is cut at its end. The
  name the cut falls in can still lose its start.
- A path or URL shown as text, cut or not, takes `dir="ltr"` and renders through
  `PathText` (`src/components/PathText.tsx`), which keeps each folder name, and each
  part of an http(s) URL's query and fragment, in its own direction; without it,
  folders named in Arabic swap places. An email cut with
  `truncate` takes `dir={emailDir(email)}` (`src/lib/direction.ts`), which keeps one
  with an Arabic name part in the page's direction; `dir="auto"` would turn it right
  to left on a left-to-right page. Size a cut one as for a name, above; a path that
  wraps instead follows the path or URL rule above.
- A cut line that joins translated text with a name or email (`<summary> · <email>`)
  can't be one `truncate` span: the "…" falls at the line's end, so the part there loses
  its start. Render each part as its own truncating `flex` item, the translated part in
  the page's direction and the other in its own (`dir="auto"`, or `emailDir` for an
  email), each with its own `title` so the tooltip reads that way too. Let the line wrap,
  so a part that doesn't fit moves to a line of its own instead of cutting both, and hide
  the separator that would open that line, as `UserDetailHeader` does. A part that
  carries the separator's negative margin keeps the page's direction and takes its `dir`
  on an inner span, as above. `AccountRow` in `src/pages/admin-backups/RestoreUsers.tsx`
  does this.
- A line of details joined with " · " that stays on one line and is cut short (a
  collection's library, count and "by" owner) renders through `MetaLine`
  (`src/components/MetaLine.tsx`), never as one joined string. User text in a
  translated part (a name, a note) goes to `t()` as a `LineName`. Each part is cut
  with its own "…" and keeps a letter of it, the part at the line's end is cut first,
  and screen readers read the line as one string.
- Lucide icons that point along the reading direction (chevrons, arrows, undo,
  sign in/out, send) mirror through one rule in `app.css`; play, skip, rotate, clock
  and brand icons don't. Add `no-mirror` to keep one physical. A caret that also
  rotates needs an `rtl:` rotation, because `scale` applies before `rotate`.
- Gradients, `translate-x` and arrow keys have no logical form: add the `rtl:`
  counterpart (`rtl:bg-gradient-to-l`, `rtl:-translate-x-…`), multiply by
  `var(--dir-sign)` in CSS, or read keys through `readingArrowKey`. `rtl:` is
  redefined in `app.css` so that, like the icon rule, it stops at a `dir="ltr"` box.
- Check a change under `ar-XB` (above).

## Tests

Tests render with the real English catalog (`test-setup.ts`), so they keep asserting
today's English. A formatting error (a missing placeholder value) throws. Don't write
tests that match translated strings.

## Bundle

English is in the main bundle (every message's fallback); other languages are lazy
chunks, and messages are precompiled, so the ICU parser is aliased away in
`vite.config.ts`.

Admin text (`web.admin.*`) is a separate part of every catalog (the `catalogParts`
plugin in `vite.config.ts`), so the launch bundle doesn't carry it:

- `AdminLayout` and the setup wizard call `useAdminMessages()` and show a loading
  state until the part is loaded; an admin's session asks for it at sign-in.
- An admin surface outside the layout (the server activity popover, the seek-preview
  dialog) calls `useAdminMessages({ optional: true })` and renders nothing until the
  part is ready. A failed load hides it instead of reaching the error boundary.
- Text that a non-admin can see belongs outside `web.admin.*`, even when an admin
  component shows it too (`web.common.*`). A `web.admin.*` message formatted before
  its part loads renders empty and asks for the part.
- The `silo-admin-text/no-admin-message` lint rule enforces this: `web.admin.*` ids
  are allowed only in the paths listed in `ADMIN_TEXT_PATHS` (`eslint.config.js`).
  Tests are not checked, because `test-setup.ts` preloads the admin part.
