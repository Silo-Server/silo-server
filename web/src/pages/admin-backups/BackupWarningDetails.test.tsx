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

/**
 * Every toggle label, closed then opened: accounts, missing files, and the one
 * group of missing files (which appears once the files are open).
 */
function toggleLabels(tag: string, messages: Messages, count: number): string[] {
  const intl = createAppIntl(tag, messages);
  function Wrapper({ children }: { children: ReactNode }) {
    return <AppIntlProvider intl={intl}>{children}</AppIntlProvider>;
  }
  render(<BackupWarningDetails backup={backup(count)} />, { wrapper: Wrapper });
  const [accounts, files] = screen.getAllByRole("button", { expanded: false });
  if (!accounts || !files) throw new Error("missing a toggle");
  const labels = [accounts.textContent ?? "", files.textContent ?? ""];
  fireEvent.click(files);
  const group = screen.getAllByRole("button", { expanded: false }).at(-1);
  if (!group || group === accounts) throw new Error("no file-group toggle");
  labels.push(group.textContent ?? "", files.textContent ?? "");
  fireEvent.click(accounts);
  fireEvent.click(group);
  labels.push(accounts.textContent ?? "", group.textContent ?? "");
  cleanup();
  return labels;
}

describe("BackupWarningDetails toggles", () => {
  // The labels print no number, so their singular is for exactly one item.
  // Under Russian plural rules, CLDR's "one" also covers 21 and 101.
  it("use the singular only for exactly one", () => {
    const messages = pseudo as unknown as Messages;
    const several = toggleLabels("ru", messages, 5);
    const one = toggleLabels("ru", messages, 1);
    one.forEach((label, i) => expect(label).not.toBe(several[i]));
    expect(toggleLabels("ru", messages, 21)).toEqual(several);
    expect(toggleLabels("ru", messages, 101)).toEqual(several);
  });

  it("pass exactly two for a language's dual", () => {
    expect(toggleLabels("ar", dualCatalog(), 1)).toEqual(Array(6).fill("one"));
    expect(toggleLabels("ar", dualCatalog(), 2)).toEqual(Array(6).fill("two"));
    expect(toggleLabels("ar", dualCatalog(), 5)).toEqual(Array(6).fill("many"));
  });
});
