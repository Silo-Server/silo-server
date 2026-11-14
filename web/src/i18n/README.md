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
3. Replace the literals (below), then add the area's files or directory to
   `I18N_CONVERTED` in `web/eslint.config.js`, one glob per line, sorted. From then on
   ESLint rejects literal JSX text, literal text in visible props (`label`, `title`,
   `description`, `help`, `placeholder`, …, `aria-*`) and literal toast messages there.
   It can't see strings a plain helper returns; the pseudo-locale pass below does.
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
  place. `select` and `plural` arguments are not. English bytes never change.
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
  a profile's stored choice applies any language in `SHIPPED_LANGUAGES`. For those eight,
  shipping takes only the regenerate; a new language also needs its row there.
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
  always left to right; inline `code`, `kbd` and `samp` take their text's direction.
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
