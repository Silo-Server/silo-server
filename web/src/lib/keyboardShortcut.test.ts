import { afterEach, describe, expect, it, vi } from "vitest";

function stubUserAgent(value: string) {
  vi.spyOn(window.navigator, "userAgent", "get").mockReturnValue(value);
}

async function loadShortcutLabel(userAgent: string) {
  vi.resetModules();
  stubUserAgent(userAgent);
  const { SEARCH_SHORTCUT_LABEL } = await import("./keyboardShortcut");
  return SEARCH_SHORTCUT_LABEL;
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe("SEARCH_SHORTCUT_LABEL", () => {
  it.each([
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15",
    "Mozilla/5.0 (iPad; CPU OS 17_0 like Mac OS X) AppleWebKit/605.1.15",
  ])("uses the command glyph on Apple keyboards", async (userAgent) => {
    expect(await loadShortcutLabel(userAgent)).toBe("⌘ K");
  });

  it.each([
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36",
    "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36",
    "Mozilla/5.0 (Linux; Android 14) AppleWebKit/537.36",
  ])("spells out Ctrl everywhere else", async (userAgent) => {
    expect(await loadShortcutLabel(userAgent)).toBe("Ctrl K");
  });
});
