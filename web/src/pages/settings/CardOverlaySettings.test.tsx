// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { CardOverlaySettings } from "./CardOverlaySettings";

// The badge size slider (Radix) measures itself with ResizeObserver.
class ResizeObserverStub {
  observe() {}
  unobserve() {}
  disconnect() {}
}
if (typeof globalThis.ResizeObserver === "undefined") {
  (globalThis as unknown as { ResizeObserver: typeof ResizeObserverStub }).ResizeObserver =
    ResizeObserverStub;
}

vi.mock("@/hooks/queries/ratingsCapability", () => ({
  useShownRatingSources: () => new Set(["imdb", "tmdb"]),
}));

const mocks = vi.hoisted(() => ({
  overlaysEnabled: true,
  quickActionsEnabled: false,
  hasBadgeOverride: false,
  hasQuickActionOverride: false,
  resetPrefs: vi.fn(),
  setPrefs: vi.fn(),
  setOverlaysEnabled: vi.fn(),
  setQuickActionsEnabled: vi.fn(),
  setQuickActionMode: vi.fn(),
}));

vi.mock("@/hooks/useOverlayPrefs", async () => {
  const { buildDefaultPrefs } = await import("@/lib/overlays");
  return {
    useOverlayPrefs: () => ({
      prefs: null,
      editablePrefs: buildDefaultPrefs(),
      images: [],
      supportsStyles: true,
      setPrefs: mocks.setPrefs,
      quickActionPreference: "favorites",
      setQuickActionMode: mocks.setQuickActionMode,
      quickActionsEnabled: mocks.quickActionsEnabled,
      setQuickActionsEnabled: mocks.setQuickActionsEnabled,
      overlaysEnabled: mocks.overlaysEnabled,
      setOverlaysEnabled: mocks.setOverlaysEnabled,
      resetPrefs: mocks.resetPrefs,
      hasBadgeOverride: mocks.hasBadgeOverride,
      hasQuickActionOverride: mocks.hasQuickActionOverride,
      isResetting: false,
      isOverlaySupported: () => true,
      isLoading: false,
    }),
  };
});

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

describe("CardOverlaySettings", () => {
  beforeEach(() => {
    mocks.overlaysEnabled = true;
    mocks.quickActionsEnabled = false;
    mocks.hasBadgeOverride = false;
    mocks.hasQuickActionOverride = false;
    mocks.resetPrefs.mockReset().mockResolvedValue(undefined);
    for (const fn of [
      mocks.setPrefs,
      mocks.setOverlaysEnabled,
      mocks.setQuickActionsEnabled,
      mocks.setQuickActionMode,
    ]) {
      fn.mockReset();
    }
  });

  afterEach(cleanup);

  it("turns shortcuts on and picks their kind with one choice", () => {
    const picked = vi.fn();
    render(<CardOverlaySettings onShortcutsPicked={picked} />);
    const shortcuts = screen.getByRole("radiogroup", { name: "Shortcut buttons" });

    // Off on the profile even though the server default may be on: still operable.
    expect(within(shortcuts).getByRole("radio", { name: "Off" })).toHaveAttribute(
      "aria-checked",
      "true",
    );
    fireEvent.click(within(shortcuts).getByRole("radio", { name: "Watched" }));

    expect(mocks.setQuickActionsEnabled).toHaveBeenCalledWith(true);
    expect(mocks.setQuickActionMode).toHaveBeenCalledWith("watched");
    expect(picked).toHaveBeenCalled();
  });

  it("turns shortcuts off without changing their kind", () => {
    mocks.quickActionsEnabled = true;
    render(<CardOverlaySettings />);
    const shortcuts = screen.getByRole("radiogroup", { name: "Shortcut buttons" });

    fireEvent.click(within(shortcuts).getByRole("radio", { name: "Off" }));

    expect(mocks.setQuickActionsEnabled).toHaveBeenCalledWith(false);
    expect(mocks.setQuickActionMode).not.toHaveBeenCalled();
  });

  it("saves the watch-status choice into the badge document, badges on or off", () => {
    mocks.overlaysEnabled = false;
    render(<CardOverlaySettings />);
    const status = screen.getByRole("radiogroup", { name: "Watch status" });

    expect(
      within(status).getByRole("radio", { name: "Watched and episodes left" }),
    ).toHaveAttribute("aria-checked", "true");
    fireEvent.click(within(status).getByRole("radio", { name: "Watched only" }));

    expect(mocks.setPrefs).toHaveBeenCalledTimes(1);
    const saved = mocks.setPrefs.mock.calls[0]?.[0];
    expect(saved.watchStatus).toBe("watched");
    // The rest of the document is the profile's own, not a reset.
    expect(saved.items.resolution.enabled).toBe(true);
  });

  it("hides the badge list while badges are off but keeps their switch", () => {
    mocks.overlaysEnabled = false;
    render(<CardOverlaySettings />);

    expect(screen.queryByRole("radiogroup", { name: "Badge style" })).not.toBeInTheDocument();
    expect(screen.queryByText("IMDb Rating")).not.toBeInTheDocument();
    const toggle = screen.getByRole("switch", { name: "Show badges on posters" });
    expect(toggle).toBeEnabled();
    fireEvent.click(toggle);
    expect(mocks.setOverlaysEnabled).toHaveBeenCalledWith(true);
  });

  it("folds badges by category and switches one inside", () => {
    render(<CardOverlaySettings />);

    expect(screen.queryByRole("switch", { name: "IMDb Rating" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: /Ratings & Certifications/ }));
    const imdb = screen.getByRole("switch", { name: "IMDb Rating" });
    const wasOn = imdb.getAttribute("aria-checked") === "true";
    expect(screen.queryByRole("button", { name: "Adjust IMDb Rating" }) !== null).toBe(wasOn);

    fireEvent.click(imdb);

    expect(mocks.setPrefs).toHaveBeenCalledWith(
      expect.objectContaining({
        items: expect.objectContaining({
          rating_imdb: expect.objectContaining({ enabled: !wasOn }),
        }),
      }),
    );
  });

  it("resets shortcuts on their own once they differ from the server's", () => {
    const { rerender } = render(<CardOverlaySettings />);
    expect(screen.queryByRole("button", { name: "Reset shortcuts" })).toBeNull();

    mocks.hasQuickActionOverride = true;
    rerender(<CardOverlaySettings />);
    fireEvent.click(screen.getByRole("button", { name: "Reset shortcuts" }));

    expect(mocks.resetPrefs).toHaveBeenCalledWith("quickActions");
    // Badges are a separate choice and keep their own Reset.
    expect(screen.queryByRole("button", { name: "Reset badges" })).toBeNull();
  });

  it("asks before resetting badges, then resets only badges", async () => {
    mocks.hasBadgeOverride = true;
    render(<CardOverlaySettings />);
    expect(screen.queryByRole("button", { name: "Use server defaults" })).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Reset badges" }));
    expect(mocks.resetPrefs).not.toHaveBeenCalled();
    const dialog = await screen.findByRole("alertdialog");
    fireEvent.click(within(dialog).getByRole("button", { name: "Reset badges" }));

    await waitFor(() => expect(mocks.resetPrefs).toHaveBeenCalledWith("badges"));
    expect(mocks.resetPrefs).toHaveBeenCalledTimes(1);
  });
});
