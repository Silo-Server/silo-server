import type { ReactNode } from "react";
import { RawIntlProvider, type IntlShape } from "react-intl";

/** The app's formatter for its subtree; `LocaleProvider` renders it. */
export function AppIntlProvider({ intl, children }: { intl: IntlShape; children?: ReactNode }) {
  return <RawIntlProvider value={intl}>{children}</RawIntlProvider>;
}
