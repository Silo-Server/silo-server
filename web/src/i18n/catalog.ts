import type { MessageFormatElement } from "react-intl";

import type { MessageKey } from "@/locales/keys";

/** A generated catalog: message id → precompiled ICU AST. */
export type Messages = Partial<Record<string, MessageFormatElement[]>>;

/**
 * Pseudo-locales (`en-XA` accented and longer, `ar-XB` right-to-left) for
 * finding unconverted text, clipping and mirroring bugs. Only builds with the
 * flag (and `vite dev`) bundle them or let a stored tag select them.
 */
export const PSEUDO_LOCALES_ENABLED =
  import.meta.env.DEV || import.meta.env.VITE_PSEUDO_LOCALES === "1";

// English is the source and every message's fallback, so it ships in the main
// bundle. Each other language is its own lazy chunk.
const english = import.meta.glob<Messages>("../locales/en.json", {
  eager: true,
  import: "default",
})["../locales/en.json"]!;

const translations = import.meta.glob<Messages>(
  ["../locales/*.json", "!../locales/en.json", "!../locales/*-X?.json"],
  { import: "default" },
);
// A static `false` drops the glob, and the pseudo chunks, from the build.
const pseudoTranslations = PSEUDO_LOCALES_ENABLED
  ? import.meta.glob<Messages>("../locales/*-X?.json", { import: "default" })
  : {};

/** The English message for an id; the generated `keys.ts` guarantees it exists. */
export function englishMessage(id: MessageKey): MessageFormatElement[] {
  return english[id]!;
}

/**
 * The translated messages for a language or pseudo-locale, without the English
 * fallback; empty for English and for a language whose catalog is missing.
 */
export async function loadTranslations(tag: string): Promise<Messages> {
  if (tag === "en") return {};
  const path = `../locales/${tag}.json`;
  const load = translations[path] ?? pseudoTranslations[path];
  return load ? load() : {};
}
