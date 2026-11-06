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
  (`formatLocale()` / `appLanguage()` from `@/lib/appLocale`). Pass a number that is
  not a plural count as a string too, or ICU groups it (`2,026`).
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

- `CATALOG_LANGUAGES` in `src/lib/appLocale.ts` lists the languages whose translations
  have shipped. Only these follow the browser's language and appear in the pickers; a
  profile's stored choice applies any shipped language. The PR that
  ships a language's translations adds it.
- Pseudo-locales are bundled only in `vite dev` and builds with
  `VITE_PSEUDO_LOCALES=1`. There, the pickers offer them and a stored `en-XA`
  (accented, longer) or `ar-XB` (right to left) applies; a sandbox can also set the
  profile's `ui.app_language` to one through the settings API.

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

- `AdminLayout` and the setup wizard call `useAdminMessages()` and render once the
  part is loaded; an admin's session asks for it at sign-in.
- Text that a non-admin can see belongs outside `web.admin.*`, even when an admin
  component shows it too (`web.common.*`). A `web.admin.*` message formatted before
  its part loads renders empty and asks for the part.
