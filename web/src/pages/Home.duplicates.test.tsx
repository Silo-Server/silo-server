// @vitest-environment jsdom

import { act } from "react";
import type { ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import Home from "./Home";
import type { HomeSectionItemsResponse } from "@/api/types";
import { completeHomeRow } from "@/hooks/queries/homeRowLimit";
import { sectionKeys } from "@/hooks/queries/keys";
import { removeItemFromHomeSectionCaches } from "@/hooks/queries/mediaSurfaceRefresh";
import { bumpHomeRefreshSignal } from "./homeSurfaceRefresh";

(
  globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean }
).IS_REACT_ACT_ENVIRONMENT = true;

const mockUseHomeLayout = vi.fn();
const mockFetchHomeSectionItems = vi.fn();
const mockFetchRecipeCatalog = vi.fn();

vi.mock("@/hooks/queries/sections", () => ({
  useHomeLayout: (...args: unknown[]) => mockUseHomeLayout(...args),
  fetchHomeSectionItems: (...args: unknown[]) => mockFetchHomeSectionItems(...args),
  HOME_SECTION_STALE_TIME: 10 * 60 * 1000,
  HOME_SECTION_GC_TIME: 60 * 60 * 1000,
}));

vi.mock("@/lib/recipes", () => ({
  fetchRecipeCatalog: () => mockFetchRecipeCatalog(),
}));

vi.mock("@/hooks/useDocumentTitle", () => ({
  useDocumentTitle: vi.fn(),
}));

vi.mock("react-router", () => ({
  Link: ({ children, to }: { children: ReactNode; to: string }) => <a href={to}>{children}</a>,
}));

vi.mock("@/components/TasteSeedBanner", () => ({
  default: () => <div data-kind="taste-seed" />,
}));

vi.mock("@/components/HeroBanner", () => ({
  default: () => <div data-kind="hero" />,
}));

vi.mock("@/components/SectionRow", () => ({
  default: ({
    section,
    priority,
  }: {
    section: { id: string; items: Array<{ title: string }> };
    priority?: boolean;
  }) => (
    <div
      data-kind="section-row"
      data-section-id={section.id}
      data-first-item={section.items[0]?.title}
      data-priority={String(Boolean(priority))}
    />
  ),
}));

describe("Home duplicate filtering", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);

    showLayout([]);
    mockFetchHomeSectionItems.mockReset();
    mockFetchRecipeCatalog.mockReset();
    mockFetchRecipeCatalog.mockResolvedValue({ categories: {} });
  });

  afterEach(async () => {
    await act(async () => {
      root.unmount();
    });
    container.remove();
    delete document.documentElement.dataset.sidebarVisualCollapsed;
    vi.useRealTimers();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it("drops what an earlier row shows from avoid-duplicates rows, whatever order rows load in", async () => {
    avoidDuplicatesFor("recommended_for_you", "taste_match");
    const layout = [
      { ...homeLayout("for-you"), section_type: "recommended_for_you" },
      { ...homeLayout("top-picks"), section_type: "taste_match" },
      homeLayout("recent"),
    ];
    const rows: Record<string, string[]> = {
      "for-you": ["Shared", "For You Only"],
      "top-picks": ["Shared", "Top Pick Only"],
      recent: ["Shared"],
    };
    showLayout(layout);
    const requests = deferSectionRequests();
    const respond = (sectionId: string) => {
      const request = requests.find((candidate) => candidate.sectionId === sectionId)!;
      const entry = layout.find((candidate) => candidate.id === sectionId)!;
      request.respond(sectionWith(entry, rows[sectionId]!));
    };

    await renderHome(new QueryClient());
    await waitForRequestCount(requests, 3);

    // The later rows answer first and show the shared title.
    respond("top-picks");
    respond("recent");
    await waitForRenderedFirstItems({ "top-picks": "Shared", recent: "Shared" });

    // Once the first row loads, the later avoid-duplicates row gives it up;
    // the plain row keeps it.
    respond("for-you");
    await waitForRenderedFirstItems({
      "for-you": "Shared",
      "top-picks": "Top Pick Only",
      recent: "Shared",
    });
  });

  it("reads the rest of a partial row whose first cards another row already shows", async () => {
    avoidDuplicatesFor("recommended_for_you");
    const layout = [
      homeLayout("recent"),
      { ...homeLayout("for-you"), section_type: "recommended_for_you" },
    ];
    showLayout(layout);
    mockFetchHomeSectionItems.mockImplementation(
      async (sectionId: string, options: { itemLimit?: number }) => {
        if (sectionId === "recent") return sectionWith(layout[0]!, ["Shared"]);
        return options.itemLimit === undefined
          ? sectionWith(layout[1]!, ["Shared", "For You Only"])
          : { ...sectionWith(layout[1]!, ["Shared"]), partial: true };
      },
    );

    await renderHome(new QueryClient());

    await waitForRenderedFirstItems({ recent: "Shared", "for-you": "For You Only" });
  });

  it("filters a cached completed row's later cards after every row's first cards", async () => {
    avoidDuplicatesFor("recommended_for_you", "taste_match");
    const layout = [
      { ...homeLayout("for-you"), section_type: "recommended_for_you" },
      { ...homeLayout("top-picks"), section_type: "taste_match" },
    ];
    showLayout(layout);
    const queryClient = new QueryClient();
    // For You showed one card, then loaded Shared with the rest of its row.
    queryClient.setQueryData(sectionKeys.homeItems("for-you"), {
      ...sectionWith(layout[0]!, ["For You Only", "Shared"]),
      shown: 1,
    });
    queryClient.setQueryData(
      sectionKeys.homeItems("top-picks"),
      sectionWith(layout[1]!, ["For You Only", "Shared", "Top Pick Only"]),
    );

    await renderHome(queryClient);

    await waitForRenderedFirstItems({ "for-you": "For You Only", "top-picks": "Shared" });
  });

  it("keeps cached completed rows in the order they completed after a cache edit", async () => {
    avoidDuplicatesFor("recommended_for_you", "taste_match");
    const layout = [
      { ...homeLayout("for-you"), section_type: "recommended_for_you" },
      { ...homeLayout("top-picks"), section_type: "taste_match" },
    ];
    showLayout(layout);
    const queryClient = new QueryClient();
    const complete = (entry: (typeof layout)[number], titles: string[]) =>
      queryClient.setQueryData(
        sectionKeys.homeItems(entry.id),
        completeHomeRow(sectionWith(entry, titles.slice(0, 1)), sectionWith(entry, titles)),
      );
    const now = Date.now();
    const clock = vi.spyOn(Date, "now");
    // Both rows showed X. Top Picks loaded the rest of its row before For You
    // did, then watching a card rewrote every cached row.
    clock.mockReturnValue(now - 3000);
    complete(layout[1]!, ["X", "Shared", "Top Pick Only"]);
    clock.mockReturnValue(now - 2000);
    complete(layout[0]!, ["X", "Shared", "Watched"]);
    clock.mockReturnValue(now - 1000);
    removeItemFromHomeSectionCaches(queryClient, "Watched");
    clock.mockRestore();

    await renderHome(queryClient);

    await waitForRenderedFirstItems({ "for-you": "X", "top-picks": "Shared" });
  });

  it("keeps a completed row's later cards after other rows' first cards when it loses a first card", async () => {
    avoidDuplicatesFor("recommended_for_you", "taste_match");
    const layout = [
      { ...homeLayout("for-you"), section_type: "recommended_for_you" },
      { ...homeLayout("top-picks"), section_type: "taste_match" },
    ];
    showLayout(layout);
    const queryClient = new QueryClient();
    queryClient.setQueryData(sectionKeys.homeItems("for-you"), {
      ...sectionWith(layout[0]!, ["Dismissed", "Shared"]),
      shown: 1,
    });
    queryClient.setQueryData(
      sectionKeys.homeItems("top-picks"),
      sectionWith(layout[1]!, ["Shared"]),
    );

    await renderHome(queryClient);
    await waitForRenderedFirstItems({ "for-you": "Dismissed", "top-picks": "Shared" });

    // Not interested on For You's only shown card leaves it nothing to show;
    // Shared stays a later card and Top Picks keeps it.
    await act(async () => {
      removeItemFromHomeSectionCaches(queryClient, "Dismissed");
      bumpHomeRefreshSignal(queryClient);
    });

    await waitForRenderedFirstItems({ "top-picks": "Shared" });
  });

  async function renderHome(queryClient: QueryClient) {
    await act(async () => {
      root.render(
        <QueryClientProvider client={queryClient}>
          <Home />
        </QueryClientProvider>,
      );
    });
  }

  function renderedFirstItems() {
    return Object.fromEntries(
      Array.from(container.querySelectorAll('[data-kind="section-row"]')).map((row) => [
        row.getAttribute("data-section-id"),
        row.getAttribute("data-first-item"),
      ]),
    );
  }

  async function waitForRenderedFirstItems(expected: Record<string, string>) {
    await waitFor(() => expect(renderedFirstItems()).toEqual(expected));
  }
});

interface DeferredSectionRequest {
  sectionId: string;
  signal: AbortSignal;
  resolve: (label: string) => void;
  respond: (response: unknown) => void;
  reject: (error: Error) => void;
}

// Holds every section request open until the test settles it, so a refresh
// bump can land while the first generation is still in flight.
function deferSectionRequests(): DeferredSectionRequest[] {
  const requests: DeferredSectionRequest[] = [];
  mockFetchHomeSectionItems.mockImplementation(
    (sectionId: string, options: { signal: AbortSignal }) =>
      new Promise((resolve, reject) => {
        requests.push({
          sectionId,
          signal: options.signal,
          resolve: (label) => resolve(homeSection(sectionId, label)),
          respond: resolve,
          reject,
        });
      }),
  );
  return requests;
}

async function waitForRequestCount(requests: DeferredSectionRequest[], count: number) {
  await waitFor(() => expect(requests).toHaveLength(count));
}

function showLayout(sections: unknown[]) {
  mockUseHomeLayout.mockReturnValue({
    data: { sections },
    isLoading: false,
    isError: false,
    refetch: vi.fn(),
  });
}

function avoidDuplicatesFor(...types: string[]) {
  mockFetchRecipeCatalog.mockResolvedValue({
    categories: {
      personalized: types.map((type) => ({
        type,
        category: "personalized",
        presets: [],
        avoid_duplicates: true,
        supports_rotation: false,
        admin_only: false,
      })),
    },
  });
}

// A row answering one card per title, each title its content id.
function sectionWith(
  entry: ReturnType<typeof homeLayout>,
  titles: string[],
): HomeSectionItemsResponse {
  return {
    section: {
      ...entry,
      total_count: titles.length,
      items: titles.map((title) => ({
        ...homeSection(entry.id).section.items[0]!,
        content_id: title,
        title,
      })),
    },
  };
}

function homeLayout(id: string) {
  return {
    id,
    section_type: "recently_added",
    title: id,
    featured: false,
    item_limit: 18,
    is_custom: false,
    customized: false,
  };
}

function homeSection(id: string, label = "item"): HomeSectionItemsResponse {
  return {
    section: {
      ...homeLayout(id),
      total_count: 1,
      items: [
        {
          content_id: `${id}-item`,
          type: "movie",
          title: `${id} ${label}`,
          year: 2026,
          genres: [],
          status: "matched",
          rating_imdb: null,
          overview: "",
          poster_url: "",
          poster_thumbhash: "",
          backdrop_url: "",
          backdrop_thumbhash: "",
          logo_url: "",
        },
      ],
    },
  };
}
