import { useCallback, useContext, useEffect, useSyncExternalStore, type ReactNode } from "react";
import { IntlContext, type IntlShape } from "react-intl";

import type { MessageKey } from "@/locales/keys";
import { adminLoadFailure, requestAdminMessages, subscribeAdminRequest } from "./catalog";
import {
  adminMessagesReady,
  appIntl,
  formatWith,
  serverTextWith,
  subscribeAppIntl,
  type RichValues,
  type TextValues,
} from "./format";

/** Formats a message id; rich values (web-only keys with tags) return React nodes. */
export interface Translate {
  (id: MessageKey, values?: TextValues): string;
  (id: MessageKey, values: RichValues): ReactNode;
}

/** The provider's formatter; outside one (a detached render), the current one. */
function useAppIntl(): IntlShape {
  return useContext(IntlContext) ?? appIntl();
}

/** Message formatting for a component; it re-renders when the language changes. */
export function useT(): Translate {
  const intl = useAppIntl();
  return useCallback(
    (id: MessageKey, values?: RichValues) => formatWith(intl, id, values),
    [intl],
  ) as Translate;
}

/** `serverText` for a component (see there); re-renders when the language changes. */
export function useServerText(): (
  key: string | undefined,
  args: Record<string, string> | undefined,
  serverEnglish: string,
) => string {
  const intl = useAppIntl();
  return useCallback(
    (key: string | undefined, args: Record<string, string> | undefined, serverEnglish: string) =>
      serverTextWith(intl, key, args, serverEnglish),
    [intl],
  );
}

/**
 * Re-renders a component when the app language changes. Use it in components
 * that show text from plain helpers which call `t()` (calendar headings, item
 * card lines), so that text follows the language like `useT()` text does.
 */
export function useAppLanguage(): string {
  // The formatter, not just its tag: admin text arriving keeps the language.
  return useSyncExternalStore(subscribeAppIntl, appIntl, appIntl).locale;
}

/**
 * Asks for the admin part of the catalog (`web.admin.*` text, kept out of the
 * launch bundle) and reports whether it is ready in the current language. The
 * admin layout and the setup wizard show a loading state until it is. A failed
 * load throws to the error boundary, as a failed lazy route does.
 *
 * A surface an admin reaches outside the admin layout (the server activity
 * popover, the seek-preview dialog) passes `optional`: it renders nothing until
 * the part is ready, and stays hidden if the load fails, since a failure there
 * must not take the page down. Readiness follows the language: a switch swaps in
 * the new language's messages and its admin part together, so ready text never
 * goes empty mid-switch.
 */
export function useAdminMessages({ optional = false }: { optional?: boolean } = {}): boolean {
  useEffect(requestAdminMessages, []);
  const ready = useSyncExternalStore(subscribeAppIntl, adminMessagesReady, adminMessagesReady);
  // A recorded failure arrives through the request store, not the formatter.
  const failure = useSyncExternalStore(subscribeAdminRequest, adminLoadFailure, adminLoadFailure);
  if (!ready && failure !== undefined && !optional) throw failure;
  return ready;
}
