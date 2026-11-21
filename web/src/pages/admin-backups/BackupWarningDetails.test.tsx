import type { ReactNode } from "react";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";

import type { AdminBackup } from "@/api/v2/adminBackups";
import { AppIntlProvider, createAppIntl, type Messages } from "@/i18n";
import arabic from "@/locales/ar.json";
import russian from "@/locales/ru.json";

import { BackupWarningDetails } from "./BackupWarningDetails";

afterEach(cleanup);

const languages = {
  ar: createAppIntl("ar", arabic as unknown as Messages),
  ru: createAppIntl("ru", russian as unknown as Messages),
};

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
function toggleLabels(language: keyof typeof languages, count: number): string[] {
  const intl = languages[language];
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
  // Russian's CLDR "one" category also covers 21 and 101.
  it("use the singular only for exactly one", () => {
    const several = toggleLabels("ru", 5);
    expect(several).toHaveLength(3);
    expect(toggleLabels("ru", 1)).not.toEqual(several);
    expect(toggleLabels("ru", 21)).toEqual(several);
    expect(toggleLabels("ru", 101)).toEqual(several);
  });

  it("use the dual for exactly two where the language has one", () => {
    expect(toggleLabels("ar", 2)).not.toEqual(toggleLabels("ar", 5));
  });
});
