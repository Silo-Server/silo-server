import {
  createIntl,
  createIntlCache,
  type IntlShape,
  type MessageFormatElement,
  type PrimitiveType,
} from "react-intl";
import type { ReactNode } from "react";

import type { MessageKey } from "@/locales/keys";
import { interfaceDirection } from "@/lib/appLocale";
import {
  ADMIN_MESSAGE_PREFIX,
  adminEnglishLoaded,
  englishMessage,
  requestAdminMessages,
  type Messages,
} from "./catalog";

/** Values for a plain-text message. */
export type TextValues = Record<string, PrimitiveType>;
/** Values for a message with rich-text tags (web-only keys): tag name → render function. */
export type RichValues = Record<
  string,
  PrimitiveType | ReactNode | ((chunks: ReactNode[]) => ReactNode)
>;

const cache = createIntlCache();

function onError(error: { code?: string }): void {
  // A language without a message falls back to English; that is expected.
  if (error.code === "MISSING_TRANSLATION") return;
  // In tests a wrong placeholder fails the render instead of printing raw ICU.
  if (import.meta.env.MODE === "test") throw error;
  console.error(error);
}

/**
 * The formatter for a language (or pseudo-locale) tag and its translations.
 * `messages` holds only translations: a message the language lacks formats
 * from the English AST with English plural rules, never with the language's.
 *
 * Arabic prints numbers in the digits of `formatLocale` (the browser's default
 * when it is undefined), as its dates and `Intl` numbers do: Arabic-Indic on
 * ar-EG, Latin on ar-AE. The formatter's locale names them (`ar-u-nu-arab`),
 * so the plural `#` follows, and `formatWith` converts printed numbers. Every
 * other language keeps its plain tag and its output.
 */
export function createAppIntl(tag: string, messages: Messages, formatLocale?: string): IntlShape {
  const locale =
    tag === "ar"
      ? `ar-u-nu-${new Intl.NumberFormat(formatLocale).resolvedOptions().numberingSystem}`
      : tag;
  return createIntl(
    { locale, defaultLocale: "en", messages: messages as IntlShape["messages"], onError },
    cache,
  );
}

let current = createAppIntl("en", {});
/** Whether `current` carries its language's admin part (see `adminMessagesReady`). */
let currentHasAdmin = false;
const listeners = new Set<() => void>();

/** The formatter the app uses now; `LocaleProvider` replaces it on a language change. */
export function appIntl(): IntlShape {
  return current;
}

/**
 * Called by `LocaleProvider` once a language's messages have loaded; `admin`
 * says they include the language's admin part.
 */
export function setAppIntl(intl: IntlShape, options: { admin?: boolean } = {}): void {
  const admin = options.admin ?? false;
  if (intl === current && admin === currentHasAdmin) return;
  current = intl;
  currentHasAdmin = admin;
  for (const listener of listeners) listener();
}

/**
 * Whether admin text renders in the current language: the English admin part
 * has loaded and, outside English, so has the language's own.
 */
export function adminMessagesReady(): boolean {
  return adminEnglishLoaded() && (currentHasAdmin || current.locale === "en");
}

/** Calls `listener` whenever the formatter changes, so text from `t()` can re-render. */
export function subscribeAppIntl(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

// --- Bidi isolation ----------------------------------------------------------

const FSI = "\u2068";
const PDI = "\u2069";
const ARGUMENT = 1;
const SELECT = 5;
const PLURAL = 6;
const TAG = 8;

interface ArgumentUse {
  /** Arguments printed as text (`{name}`). */
  printed: Set<string>;
  /** Arguments a `select` or `plural` chooses on; never isolated, or no branch would match. */
  selectors: Set<string>;
}

const uses = new WeakMap<MessageFormatElement[], ArgumentUse>();

function argumentUse(ast: MessageFormatElement[]): ArgumentUse {
  let use = uses.get(ast);
  if (use) return use;
  use = { printed: new Set(), selectors: new Set() };
  const walk = (elements: MessageFormatElement[]) => {
    for (const element of elements) {
      if (element.type === ARGUMENT) use!.printed.add(element.value);
      else if (element.type === SELECT || element.type === PLURAL) {
        use!.selectors.add(element.value);
        for (const option of Object.values(element.options)) walk(option.value);
      } else if (element.type === TAG) walk(element.children);
    }
  };
  walk(ast);
  uses.set(ast, use);
  return use;
}

/** The message's translation in this formatter's language, if it has one. */
function translation(intl: IntlShape, id: string): MessageFormatElement[] | undefined {
  const translated = intl.messages[id];
  return Array.isArray(translated) ? translated : undefined;
}

// --- Digits -------------------------------------------------------------------

interface LocalDigits {
  system: string;
  /** "0" to "9" in this system. */
  digits: string[];
  /** Numbers without grouping, as `String(n)` prints them. */
  numbers: Intl.NumberFormat;
}

const digitsByLocale = new Map<string, LocalDigits | null>();

/** The formatter's digits when they aren't Latin (Arabic on ar-EG), else null. */
function nonLatinDigits(intl: IntlShape): LocalDigits | null {
  let entry = digitsByLocale.get(intl.locale);
  if (entry === undefined) {
    const system = new Intl.Locale(intl.locale).numberingSystem;
    if (system && system !== "latn") {
      const numbers = new Intl.NumberFormat(intl.locale, {
        useGrouping: false,
        maximumFractionDigits: 20,
      });
      const digits = Array.from({ length: 10 }, (_, digit) => numbers.format(digit));
      entry = { system, digits, numbers };
    } else {
      entry = null;
    }
    digitsByLocale.set(intl.locale, entry);
  }
  return entry;
}

/**
 * The numbering system the formatter names (Arabic's `latn` or `arab`), for
 * an `Intl` formatter keyed by the interface language, such as relative times;
 * undefined for a language that keeps its own.
 */
export function messageNumberingSystem(intl: IntlShape = current): string | undefined {
  return new Intl.Locale(intl.locale).numberingSystem;
}

/**
 * A number shown outside a message (a year, a year range), in the digits
 * messages use: unchanged except in Arabic on a format locale with
 * Arabic-Indic digits. Never pass a value that is sent, matched, compared or
 * put in a URL; those keep their Latin digits.
 */
export function numberText(value: number | string, intl: IntlShape = current): string {
  const local = nonLatinDigits(intl);
  if (!local) return String(value);
  if (typeof value === "number") return local.numbers.format(value);
  return value.replace(/[0-9]/g, (digit) => local.digits[Number(digit)] ?? digit);
}

/** A bare number ("01"), which takes its direction from the letters around it. */
const DIGITS = /^\p{Nd}+$/u;

/**
 * Prepares the printed values of a message (selector values are left alone,
 * or no branch would match):
 *
 * - When the formatter's digits aren't Latin, a number value prints in those
 *   digits, as the plural `#` does. Only in the language's own translation:
 *   an English fallback prints `#` in Latin, so its values stay Latin too.
 *   Strings are never changed; pass a number shown as a number.
 * - In a right-to-left interface, every value printed as text is wrapped in
 *   Unicode isolates so a left-to-right title keeps its punctuation in place. Bare
 *   numbers ("01" in "S{season}E{episode}") are not: an isolate would cut a
 *   number off the letters it belongs to and reorder it. A range or a signed
 *   number ("2-3", "8+") keeps its isolate, or Arabic text would reorder it.
 *
 * Left-to-right output in Latin digits is unchanged, so English keeps its bytes.
 */
function prepareValues<V extends RichValues>(
  intl: IntlShape,
  ast: MessageFormatElement[],
  values: V | undefined,
  translated: boolean,
): V | undefined {
  if (!values) return values;
  const rtl = interfaceDirection(intl.locale) === "rtl";
  const localDigits = translated && nonLatinDigits(intl) !== null;
  if (!rtl && !localDigits) return values;
  const { printed, selectors } = argumentUse(ast);
  const prepared: RichValues = { ...values };
  for (const name of printed) {
    if (selectors.has(name)) continue;
    let value = values[name];
    if (typeof value === "number") value = localDigits ? numberText(value, intl) : String(value);
    if (rtl && typeof value === "string" && !DIGITS.test(value)) value = FSI + value + PDI;
    prepared[name] = value;
  }
  return prepared as V;
}

// --- Formatting -------------------------------------------------------------

export function formatWith(intl: IntlShape, id: MessageKey, values?: TextValues): string;
export function formatWith(intl: IntlShape, id: MessageKey, values?: RichValues): ReactNode;
export function formatWith(intl: IntlShape, id: MessageKey, values?: RichValues): ReactNode {
  // A label table looked up with a value it doesn't list (an enum value the
  // server added) gives no id. Show nothing, as the English tables did, rather
  // than fail the render; tests still fail so a missing entry is noticed.
  if (id === undefined) {
    if (import.meta.env.MODE === "test") throw new Error("formatWith: no message id");
    return "";
  }
  const english = englishMessage(id);
  if (!english) {
    // Admin text before its part has loaded. Ask for it; components using
    // `useT()` or `useAppLanguage()` re-render when it arrives.
    if (import.meta.env.MODE === "test") throw new Error(`formatWith: ${id} is not loaded`);
    // Deferred: asking notifies LocaleProvider, which must not update during a render.
    if (id.startsWith(ADMIN_MESSAGE_PREFIX)) queueMicrotask(requestAdminMessages);
    return "";
  }
  const translated = translation(intl, id);
  return intl.formatMessage(
    { id, defaultMessage: english },
    prepareValues(intl, translated ?? english, values, translated !== undefined) as Record<
      string,
      PrimitiveType
    >,
  );
}

/**
 * Formats a message in the current language, for code outside components
 * (plain helpers, error classes). Call it when the text is used, never at
 * module scope: the result is fixed in the language of that moment.
 * Components use `useT()`, which re-renders on a language change.
 */
export function t(id: MessageKey, values?: TextValues): string {
  return formatWith(current, id, values);
}

const english = createAppIntl("en", {});

/**
 * A message in English whatever the interface language, for text that is data:
 * stored, sent to the server, matched or compared (it stays English everywhere).
 */
export function englishText(id: MessageKey, values?: TextValues): string {
  return formatWith(english, id, values);
}

/**
 * Text the server sent in English, translated when the interface language's
 * own catalog has `key` (a server-provided id such as a problem type or a
 * section's `title_key`) and `args` fill every placeholder it uses. Otherwise,
 * and always in English, the server's text exactly as sent.
 */
export function serverTextWith(
  intl: IntlShape,
  key: string | undefined,
  args: Record<string, string> | undefined,
  serverEnglish: string,
): string {
  if (!key) return serverEnglish;
  const ast = translation(intl, key);
  if (!ast) return serverEnglish;
  const { printed, selectors } = argumentUse(ast);
  for (const name of [...printed, ...selectors]) {
    if (typeof args?.[name] !== "string") return serverEnglish;
  }
  return intl.formatMessage({ id: key }, prepareValues(intl, ast, args, true));
}

/** `serverTextWith` in the current language, for code outside components. */
export function serverText(
  key: string | undefined,
  args: Record<string, string> | undefined,
  serverEnglish: string,
): string {
  return serverTextWith(current, key, args, serverEnglish);
}
