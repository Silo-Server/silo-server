import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import type { WatchlistTitle } from "@/api/v2/watchlistTitles";
import { UICustomizationContext } from "@/contexts/uiCustomizationContext";
import {
  buildDefaultPrefs,
  PRESET_IDS,
  type CardOverlayPrefs,
  type OverlayId,
  type PresetId,
} from "@/lib/overlays";
import type { CardCaption } from "@/lib/uiCustomization";
import { watchlistTitleStatus } from "@/lib/watchlistTitles";
import WatchlistTitleCard from "./WatchlistTitleCard";

const NOW = new Date(2026, 8, 30, 12);

const downloading: WatchlistTitle = {
  media_type: "movie",
  tmdb_id: 949,
  title: "Heat",
  year: 1995,
  added_at: "2026-09-01T00:00:00Z",
  status: "active",
  request: {
    requestable: false,
    status: "downloading",
    download: { phase: "downloading", percent: 43, downloads: 1, updated_at: NOW.toISOString() },
  },
};

const lost: WatchlistTitle = {
  ...downloading,
  tmdb_id: 950,
  title: "Lost Film",
  status: "removed",
  request: { requestable: false },
};

const notRequested: WatchlistTitle = {
  ...downloading,
  tmdb_id: 951,
  title: "Open Title",
  request: { requestable: true },
};

function prefsWith(preset: PresetId, only?: OverlayId): CardOverlayPrefs {
  const prefs = buildDefaultPrefs();
  prefs.preset = preset;
  if (only) {
    for (const key of Object.keys(prefs.items) as OverlayId[]) {
      prefs.items[key] = { ...prefs.items[key], enabled: key === only };
    }
  }
  return prefs;
}

function renderCard(
  title: WatchlistTitle,
  overlayPrefs: CardOverlayPrefs | null,
  options: { caption?: CardCaption; onRequest?: () => void; onRemove?: () => void } = {},
): HTMLElement {
  const wrap = (children: ReactNode) => (
    <UICustomizationContext.Provider
      value={{
        cardPresentation: { poster_size: "large", caption: options.caption ?? "title_metadata" },
        cardPresentationSource: "profile_client",
        primaryMenu: null,
        primaryMenuSource: "default",
        shortcuts: { items: [] },
        isSupported: true,
        supportsAtomicShortcuts: true,
        isLoading: false,
        isUnavailable: false,
      }}
    >
      <MemoryRouter>{children}</MemoryRouter>
    </UICustomizationContext.Provider>
  );
  return render(
    wrap(
      <WatchlistTitleCard
        title={title}
        status={watchlistTitleStatus(title, NOW)}
        overlayPrefs={overlayPrefs}
        onRequest={options.onRequest}
        onRemove={options.onRemove}
      />,
    ),
  ).container;
}

describe("WatchlistTitleCard", () => {
  beforeEach(() => vi.stubGlobal("CSS", { supports: () => true }));
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("opens the Discover title page and never offers Play", () => {
    renderCard(downloading, buildDefaultPrefs());
    for (const link of screen.getAllByRole("link", { name: /Heat/ })) {
      expect(link).toHaveAttribute("href", "/title/movie/949");
    }
    expect(screen.queryByRole("button", { name: /play/i })).toBeNull();
  });

  const captionCases: Array<[string, CardOverlayPrefs | null]> = [
    ["overlays off", null],
    ["the status badge off", prefsWith("classic", "content_rating")],
    ...PRESET_IDS.map((id): [string, CardOverlayPrefs] => [`the ${id} preset`, prefsWith(id)]),
  ];

  it.each(captionCases)("keeps the full status in the caption with %s", (_, prefs) => {
    for (const caption of ["title_metadata", "title", "artwork"] as const) {
      renderCard(downloading, prefs, { caption });
      expect(screen.getByTestId("watchlist-title-caption").textContent).toMatch(
        /^Downloading · 43%/,
      );
      cleanup();
    }
  });

  it("draws the download bar only while overlays and the status badge are on", () => {
    const prefs = buildDefaultPrefs();
    prefs.items.request_status = { ...prefs.items.request_status, position: "bottom-left" };
    const withBar = renderCard(downloading, prefs);
    expect(withBar.querySelector("[data-request-download-bar]")).not.toBeNull();
    // The bottom badge row rises clear of the bar.
    expect(withBar.querySelector('[data-overlay-edge="bottom"]')).toHaveClass("mb-2");
    cleanup();

    expect(renderCard(downloading, null).querySelector("[data-request-download-bar]")).toBeNull();
    cleanup();

    const badgeOff = buildDefaultPrefs();
    badgeOff.items.request_status = { ...badgeOff.items.request_status, enabled: false };
    expect(
      renderCard(downloading, badgeOff).querySelector("[data-request-download-bar]"),
    ).toBeNull();
  });

  it("marks a title that needs attention in amber with a way to find it again", () => {
    renderCard(lost, buildDefaultPrefs());
    const caption = screen.getByTestId("watchlist-title-caption");
    expect(caption).toHaveClass("text-amber-300");
    expect(caption.textContent).toBe("No longer listed on TMDB · Find it");
    expect(screen.getByRole("link", { name: "Find it" }).getAttribute("href")).toContain(
      "q=Lost+Film",
    );
  });

  it("offers Request on hover only for a title that can be requested", () => {
    const onRequest = vi.fn();
    renderCard(notRequested, null, { onRequest });
    fireEvent.click(screen.getByRole("button", { name: /Request Open Title/ }));
    expect(onRequest).toHaveBeenCalledTimes(1);
    cleanup();

    renderCard(downloading, null, { onRequest });
    expect(screen.queryByRole("button", { name: /Request Heat/ })).toBeNull();
  });

  it("removes the title from the watchlist from the corner action", () => {
    const onRemove = vi.fn();
    renderCard(downloading, null, { onRemove });
    const button = screen.getByRole("button", { name: "Remove Heat from your watchlist" });
    expect(button).toHaveAttribute("aria-pressed", "true");
    fireEvent.click(button);
    expect(onRemove).toHaveBeenCalledTimes(1);
  });
});
