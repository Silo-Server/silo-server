import { render } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";

import { resolveAppLocale, setAppLocale } from "@/lib/appLocale";
import { Calendar } from "./calendar";

function applyLanguage(stored: string, runtimeLocale: string) {
  setAppLocale(
    resolveAppLocale({
      stored,
      navigatorLanguages: [],
      runtimeLocale,
      hasCatalog: () => true,
      pseudoLocales: false,
    }),
  );
}

// The weekday header's aria-label is react-day-picker's English day name.
function firstWeekday(): string | null {
  const { container } = render(<Calendar month={new Date(2026, 10, 1)} />);
  return container.querySelector("thead th")?.getAttribute("aria-label") ?? null;
}

describe("Calendar", () => {
  afterEach(() => applyLanguage("en", "en-US"));

  it.each([
    ["pl", "en-US", "Monday"],
    ["ar", "en-US", "Saturday"],
    ["en-US", "de-DE", "Sunday"],
  ])("starts the week where %s does", (stored, runtimeLocale, weekday) => {
    applyLanguage(stored, runtimeLocale);
    expect(firstWeekday()).toBe(weekday);
  });
});
