import { useCallback, useContext, useSyncExternalStore, type ReactNode } from "react";
import { IntlContext, type IntlShape } from "react-intl";

import type { MessageKey } from "@/locales/keys";
import {
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
  return useSyncExternalStore(subscribeAppIntl, appLanguageTag, appLanguageTag);
}

function appLanguageTag(): string {
  return appIntl().locale;
}
