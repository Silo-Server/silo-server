import {
  createIntl,
  createIntlCache,
  type IntlShape,
  type MessageFormatElement,
  type PrimitiveType,
} from "react-intl";
import type { ReactNode } from "react";

import { LOCALES, type MessageKey } from "@/locales/keys";
import { englishMessage, type Messages } from "./catalog";

/** Values for a plain-text message. */
export type TextValues = Record<string, PrimitiveType>;
/** Values for a message with rich-text tags (web-only keys): tag name → render function. */
export type RichValues = Record<
  string,
  PrimitiveType | ReactNode | ((chunks: ReactNode[]) => ReactNode)
>;

const cache = createIntlCache();
const RTL_TAGS = new Set(LOCALES.filter((locale) => locale.dir === "rtl").map(({ tag }) => tag));

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
 */
export function createAppIntl(tag: string, messages: Messages): IntlShape {
  return createIntl(
    { locale: tag, defaultLocale: "en", messages: messages as IntlShape["messages"], onError },
    cache,
  );
}

let current = createAppIntl("en", {});
const listeners = new Set<() => void>();

/** The formatter the app uses now; `LocaleProvider` replaces it on a language change. */
export function appIntl(): IntlShape {
  return current;
}

/** Called by `LocaleProvider` once a language's messages have loaded. */
export function setAppIntl(intl: IntlShape): void {
  if (intl === current) return;
  current = intl;
  for (const listener of listeners) listener();
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

/**
 * In a right-to-left interface, wraps each printed string value in Unicode
 * isolates so a left-to-right title keeps its punctuation in place. Selector
 * values and left-to-right output are left alone, so English keeps its bytes.
 */
function isolateValues<V extends RichValues>(
  intl: IntlShape,
  ast: MessageFormatElement[],
  values: V | undefined,
): V | undefined {
  if (!values || !RTL_TAGS.has(intl.locale)) return values;
  const { printed, selectors } = argumentUse(ast);
  const isolated: RichValues = { ...values };
  for (const name of printed) {
    const value = values[name];
    if (typeof value === "string" && !selectors.has(name)) isolated[name] = FSI + value + PDI;
  }
  return isolated as V;
}

// --- Formatting -------------------------------------------------------------

export function formatWith(intl: IntlShape, id: MessageKey, values?: TextValues): string;
export function formatWith(intl: IntlShape, id: MessageKey, values?: RichValues): ReactNode;
export function formatWith(intl: IntlShape, id: MessageKey, values?: RichValues): ReactNode {
  const english = englishMessage(id);
  const ast = translation(intl, id) ?? english;
  return intl.formatMessage(
    { id, defaultMessage: english },
    isolateValues(intl, ast, values) as Record<string, PrimitiveType>,
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
  return intl.formatMessage({ id: key }, isolateValues(intl, ast, args));
}

/** `serverTextWith` in the current language, for code outside components. */
export function serverText(
  key: string | undefined,
  args: Record<string, string> | undefined,
  serverEnglish: string,
): string {
  return serverTextWith(current, key, args, serverEnglish);
}
