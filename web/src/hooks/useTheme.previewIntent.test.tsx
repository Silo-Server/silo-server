// @vitest-environment jsdom

import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/hooks/queries/settingValues", () => ({
  useEffectiveSettings: () => ({ data: undefined }),
}));

vi.mock("@/hooks/queries/profileDefaults", () => ({
  useProfileDefaultWriter: () => ({ save: vi.fn() }),
}));

vi.mock("@/hooks/useBranding", () => ({
  useBranding: () => ({ defaultTheme: null }),
}));

// Only the auth-dependent hook is replaced; the parsing helpers around it are
// the real ones so this exercises the provider's actual resolution order.
vi.mock("@/hooks/themePreferences", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./themePreferences")>()),
  useAppearanceCacheOwner: () => null,
}));

import { THEME_PREVIEW_INTENT_MS, ThemeProvider, useTheme } from "./useTheme";

function Probe() {
  const { theme, previewTheme, resetPreviewTheme, setTheme } = useTheme();
  return (
    <>
      <span data-testid="theme">{theme}</span>
      <button type="button" onClick={() => previewTheme("cinema-light")}>
        preview light
      </button>
      <button type="button" onClick={resetPreviewTheme}>
        leave
      </button>
      <button type="button" onClick={() => setTheme("cobalt-studio")}>
        apply cobalt
      </button>
    </>
  );
}

const domTheme = () => document.documentElement.getAttribute("data-theme");

describe("theme preview hover intent", () => {
  beforeEach(() => {
    localStorage.clear();
    vi.useFakeTimers();
    render(
      <ThemeProvider>
        <Probe />
      </ThemeProvider>,
    );
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  // A cursor crossing the swatch row on its way to another menu item used to
  // flash the whole app light for a frame or two.
  it("does not restyle the app for a hover shorter than the intent delay", () => {
    const before = domTheme();

    fireEvent.click(screen.getByRole("button", { name: "preview light" }));

    // Nothing is applied on the hover itself, nor part-way through the delay.
    expect(domTheme()).toBe(before);
    act(() => {
      vi.advanceTimersByTime(THEME_PREVIEW_INTENT_MS / 2);
    });
    expect(domTheme()).toBe(before);

    fireEvent.click(screen.getByRole("button", { name: "leave" }));
    act(() => {
      vi.advanceTimersByTime(1000);
    });

    // The armed preview is cancelled, not merely deferred.
    expect(domTheme()).toBe(before);
  });

  it("previews once the pointer has settled for the intent delay", () => {
    fireEvent.click(screen.getByRole("button", { name: "preview light" }));
    act(() => {
      vi.advanceTimersByTime(THEME_PREVIEW_INTENT_MS);
    });

    expect(domTheme()).toBe("cinema-light");

    fireEvent.click(screen.getByRole("button", { name: "leave" }));

    expect(domTheme()).not.toBe("cinema-light");
  });

  it("applies a picked theme immediately and drops any armed preview", () => {
    fireEvent.click(screen.getByRole("button", { name: "preview light" }));
    fireEvent.click(screen.getByRole("button", { name: "apply cobalt" }));

    expect(domTheme()).toBe("cobalt-studio");

    act(() => {
      vi.advanceTimersByTime(1000);
    });

    expect(domTheme()).toBe("cobalt-studio");
  });
});
