import type { ReactNode } from "react";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";

import type { AdminBackup } from "@/api/v2/adminBackups";
import { AppIntlProvider, createAppIntl, type Messages } from "@/i18n";
// CI's web tests check out only the English and pseudo-English catalogs.
import pseudo from "@/locales/en-XA.json";

import { BackupWarningDetails } from "./BackupWarningDetails";

afterEach(cleanup);

type MessageFormatElement = NonNullable<Messages[string]>[number];

const TOGGLE_KEYS = ["Accounts", "Files", "GroupFiles"].flatMap((name) => [
  `web.admin.backups.warnings.show${name}`,
  `web.admin.backups.warnings.hide${name}`,
]);

/** A catalog whose toggles say "one", "two", or "many" for each `amount` branch. */
function dualCatalog(): Messages {
  const text = (value: string): MessageFormatElement[] => [{ type: 0, value }];
  const select = {
    type: 5,
    value: "amount",
    options: {
      exactlyOne: { value: text("one") },
      exactlyTwo: { value: text("two") },
      other: { value: text("many") },
    },
  } as MessageFormatElement;
  return Object.fromEntries(TOGGLE_KEYS.map((key) => [key, [select]]));
}

/** A backup that left out `count` accounts and missed `count` library posters. */
function backup(count: number): AdminBackup {
  const n = Array.from({ length: count }, (_, i) => i);
  return {
    id: "20261121T000000.000Z-manual",
    restore_set_omitted: n.map((i) => ({
      id: `user-${i}`,
      label: `user${i}@example.com`,
      message: "row too large",
      reason: "too_large",
      section: "users",
    })),
    missing_blobs: n.map((i) => ({
      key: `library-posters/${i}.jpg`,
      source: "library poster",
      store: "assets",
    })),
    missing_blobs_total: count,
  } as AdminBackup;
}

/** The accounts, files, and file-group toggle labels, with the files section open. */
function toggleLabels(tag: string, messages: Messages, count: number): string[] {
  const intl = createAppIntl(tag, messages);
  function Wrapper({ children }: { children: ReactNode }) {
    return <AppIntlProvider intl={intl}>{children}</AppIntlProvider>;
  }
  render(<BackupWarningDetails backup={backup(count)} />, { wrapper: Wrapper });
  const [, files] = screen.getAllByRole("button", { expanded: false });
  if (!files) throw new Error("no missing-files toggle");
  fireEvent.click(files);
  const labels = screen
    .getAllByRole("button")
    .filter((button) => button.hasAttribute("aria-expanded"))
    .map((button) => button.textContent ?? "");
  cleanup();
  return labels;
}

describe("BackupWarningDetails toggles", () => {
  // The labels print no number, so their singular is for exactly one item.
  // Under Russian plural rules, CLDR's "one" also covers 21 and 101.
  it("use the singular only for exactly one", () => {
    const messages = pseudo as unknown as Messages;
    const several = toggleLabels("ru", messages, 5);
    expect(several).toHaveLength(3);
    expect(toggleLabels("ru", messages, 1)).not.toEqual(several);
    expect(toggleLabels("ru", messages, 21)).toEqual(several);
    expect(toggleLabels("ru", messages, 101)).toEqual(several);
  });

  it("pass exactly two for a language's dual", () => {
    expect(toggleLabels("ar", dualCatalog(), 1)).toEqual(["one", "one", "one"]);
    expect(toggleLabels("ar", dualCatalog(), 2)).toEqual(["two", "two", "two"]);
    expect(toggleLabels("ar", dualCatalog(), 5)).toEqual(["many", "many", "many"]);
  });
});
