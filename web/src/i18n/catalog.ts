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

/** Admin text: its messages are a separate part of every catalog (vite.config.ts `catalogParts`). */
export const ADMIN_MESSAGE_PREFIX = "web.admin.";

type Loaders = Record<string, () => Promise<Messages>>;

// English is the source and every message's fallback, so its app part ships in
// the main bundle. The admin part of every catalog, English included, and each
// other language are lazy chunks.
const english = import.meta.glob<Messages>("../locales/en.json", {
  eager: true,
  query: "?part=app",
  import: "default",
})["../locales/en.json"]!;
const englishAdmin = import.meta.glob<Messages>("../locales/en.json", {
  query: "?part=admin",
  import: "default",
})["../locales/en.json"]!;

const translations: Record<"app" | "admin", Loaders> = {
  app: import.meta.glob<Messages>(
    ["../locales/*.json", "!../locales/en.json", "!../locales/*-X?.json"],
    { query: "?part=app", import: "default" },
  ),
  admin: import.meta.glob<Messages>(
    ["../locales/*.json", "!../locales/en.json", "!../locales/*-X?.json"],
    { query: "?part=admin", import: "default" },
  ),
};
// A static `false` drops the globs, and the pseudo chunks, from the build.
const pseudoTranslations: Record<"app" | "admin", Loaders> = PSEUDO_LOCALES_ENABLED
  ? {
      app: import.meta.glob<Messages>("../locales/*-X?.json", {
        query: "?part=app",
        import: "default",
      }),
      admin: import.meta.glob<Messages>("../locales/*-X?.json", {
        query: "?part=admin",
        import: "default",
      }),
    }
  : { app: {}, admin: {} };

let adminEnglish: Messages | undefined;
let adminEnglishLoad: Promise<void> | undefined;

function loadAdminEnglish(): Promise<void> {
  adminEnglishLoad ??= englishAdmin().then(
    (messages) => {
      adminEnglish = messages;
    },
    (error: unknown) => {
      // A failed chunk load (a new release replaced it) may succeed on retry.
      adminEnglishLoad = undefined;
      throw error;
    },
  );
  return adminEnglishLoad;
}

/**
 * The English message for an id. `keys.ts` guarantees one exists, but an
 * admin message is undefined until the admin part has loaded.
 */
export function englishMessage(id: MessageKey): MessageFormatElement[] | undefined {
  return english[id] ?? adminEnglish?.[id];
}

/** Whether the English admin part is loaded, so admin text can render. */
export function adminEnglishLoaded(): boolean {
  return adminEnglish !== undefined;
}

// --- Asking for admin text ----------------------------------------------------

let adminWanted = false;
const adminListeners = new Set<() => void>();

/**
 * Asks for the admin part of the catalogs. The admin layout, the setup wizard
 * and an admin's session ask; a missing admin message asks too, as a fallback.
 * `LocaleProvider` then loads it for the current language and keeps it loaded
 * across language changes.
 */
export function requestAdminMessages(): void {
  if (adminWanted) return;
  adminWanted = true;
  for (const listener of adminListeners) listener();
}

export function adminMessagesRequested(): boolean {
  return adminWanted;
}

export function subscribeAdminRequest(listener: () => void): () => void {
  adminListeners.add(listener);
  return () => adminListeners.delete(listener);
}

let adminLoadError: unknown;

/**
 * Recorded by `LocaleProvider` when the English admin part failed to load (a
 * chunk a newer release replaced). Like a failed lazy route, it stands until
 * the page reloads.
 */
export function setAdminLoadError(error: unknown): void {
  adminLoadError = error;
  for (const listener of adminListeners) listener();
}

export function adminLoadFailure(): unknown {
  return adminLoadError;
}

function loadPart(tag: string, part: "app" | "admin"): Promise<Messages> {
  if (tag === "en") return Promise.resolve({});
  const path = `../locales/${tag}.json`;
  const load = translations[part][path] ?? pseudoTranslations[part][path];
  return load ? load() : Promise.resolve({});
}

/**
 * The translated messages for a language or pseudo-locale, without the English
 * fallback or the admin part; empty for English and for a language whose
 * catalog is missing.
 */
export function loadTranslations(tag: string): Promise<Messages> {
  return loadPart(tag, "app");
}

/**
 * The admin part of a language's translations (see `loadTranslations`). It
 * resolves only once the English admin part has loaded too, so admin text
 * never formats without its fallback, and rejects only when that fails:
 * without its own part a language shows admin text in English.
 */
export async function loadAdminTranslations(tag: string): Promise<Messages> {
  const [admin] = await Promise.all([
    loadPart(tag, "admin").catch((): Messages => ({})),
    loadAdminEnglish(),
  ]);
  return admin;
}

/** Loads the English admin part, for tests that render admin text outside `LocaleProvider`. */
export function loadAdminEnglishForTests(): Promise<void> {
  return loadAdminEnglish();
}
