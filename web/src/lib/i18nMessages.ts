import type { IntlConfig } from "react-intl";

export type Messages = NonNullable<IntlConfig["messages"]>;

// One lazy chunk per language, from the generated src/locales/<tag>.json. A
// language without a file resolves to an empty catalog, and react-intl
// renders each message's English default.
const catalogs = import.meta.glob<Messages>("../locales/*.json", { import: "default" });

function catalogPath(tag: string): string {
  return `../locales/${tag}.json`;
}

/** Whether the language has a translation catalog. */
export function hasCatalog(tag: string): boolean {
  return catalogPath(tag) in catalogs;
}

/** The compiled messages for a shipped language; empty when it has no catalog. */
export async function loadMessages(tag: string): Promise<Messages> {
  const load = catalogs[catalogPath(tag)];
  return load ? load() : {};
}
