import { afterEach, describe, expect, it, vi } from "vitest";

import { searchShortcutLabel } from "./keyboardShortcut";

function stubUserAgent(value: string) {
  vi.spyOn(window.navigator, "userAgent", "get").mockReturnValue(value);
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe("searchShortcutLabel", () => {
  it("uses the command glyph on Apple keyboards", () => {
    stubUserAgent("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15");
    expect(searchShortcutLabel()).toBe("⌘ K");

    stubUserAgent("Mozilla/5.0 (iPad; CPU OS 17_0 like Mac OS X) AppleWebKit/605.1.15");
    expect(searchShortcutLabel()).toBe("⌘ K");
  });

  it("spells out Ctrl everywhere else", () => {
    stubUserAgent("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36");
    expect(searchShortcutLabel()).toBe("Ctrl K");

    stubUserAgent("Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36");
    expect(searchShortcutLabel()).toBe("Ctrl K");

    stubUserAgent("Mozilla/5.0 (Linux; Android 14) AppleWebKit/537.36");
    expect(searchShortcutLabel()).toBe("Ctrl K");
  });
});
