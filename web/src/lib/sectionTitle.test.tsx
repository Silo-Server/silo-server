import type { ReactNode } from "react";
import { renderHook } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { AppIntlProvider, createAppIntl } from "@/i18n";
import { useSectionTitle } from "./sectionTitle";

const spanish = createAppIntl("es", {
  "home.section.continueWatching": [{ type: 0, value: "Seguir viendo" }],
  "home.section.recentlyAddedIn": [
    { type: 0, value: "Añadido en " },
    { type: 1, value: "library" },
  ],
});

function titleIn(language: "en" | "es") {
  const intl = language === "es" ? spanish : createAppIntl("en", {});
  const wrapper = ({ children }: { children: ReactNode }) => (
    <AppIntlProvider intl={intl}>{children}</AppIntlProvider>
  );
  return renderHook(() => useSectionTitle(), { wrapper }).result.current;
}

describe("useSectionTitle", () => {
  it.each([
    {
      name: "English shows the server's title",
      language: "en" as const,
      section: { title: "Continue Watching", title_key: "home.section.continueWatching" },
      expected: "Continue Watching",
    },
    {
      name: "a keyed title translates",
      language: "es" as const,
      section: { title: "Continue Watching", title_key: "home.section.continueWatching" },
      expected: "Seguir viendo",
    },
    {
      name: "arguments fill a generated title",
      language: "es" as const,
      section: {
        title: "Recently Added in Movies",
        title_key: "home.section.recentlyAddedIn",
        title_args: { library: "Movies" },
      },
      expected: "Añadido en Movies",
    },
    {
      name: "a generated title without its argument keeps the server's title",
      language: "es" as const,
      section: { title: "Recently Added in Movies", title_key: "home.section.recentlyAddedIn" },
      expected: "Recently Added in Movies",
    },
    {
      name: "a title without a key (typed by an admin) stays as typed",
      language: "es" as const,
      section: { title: "Keep Watching" },
      expected: "Keep Watching",
    },
    {
      name: "an empty key counts as none",
      language: "es" as const,
      section: { title: "Keep Watching", title_key: "" },
      expected: "Keep Watching",
    },
  ])("$name", ({ language, section, expected }) => {
    expect(titleIn(language)(section)).toBe(expected);
  });
});
