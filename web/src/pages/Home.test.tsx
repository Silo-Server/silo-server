// @vitest-environment jsdom

import { act } from "react";
import type { ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import Home from "./Home";
import { SIDEBAR_DETAILS_REVEAL_DEADLINE_MS } from "@/components/sidebarItemNavigation";
import { sectionKeys } from "@/hooks/queries/keys";
import { bumpHomeRefreshSignal } from "./homeSurfaceRefresh";

(
  globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean }
).IS_REACT_ACT_ENVIRONMENT = true;

const mockUseHomeLayout = vi.fn();
const mockFetchHomeSectionItems = vi.fn();
const SAFARI_USER_AGENT =
  "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 Version/18.6 Safari/605.1.15";
const FIREFOX_USER_AGENT =
  "Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:154.0) Gecko/20100101 Firefox/154.0";
const CHROME_USER_AGENT =
  "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 Chrome/151.0.0.0 Safari/537.36";

vi.mock("@/hooks/queries/sections", () => ({
  useHomeLayout: (...args: unknown[]) => mockUseHomeLayout(...args),
  fetchHomeSectionItems: (...args: unknown[]) => mockFetchHomeSectionItems(...args),
  HOME_SECTION_STALE_TIME: 10 * 60 * 1000,
  HOME_SECTION_GC_TIME: 60 * 60 * 1000,
}));

vi.mock("@/hooks/useDocumentTitle", () => ({
  useDocumentTitle: vi.fn(),
}));

vi.mock("react-router", () => ({
  Link: ({ children }: { children: ReactNode }) => <a>{children}</a>,
}));

vi.mock("@/components/TasteSeedBanner", () => ({
  default: () => <div data-kind="taste-seed" />,
}));

vi.mock("@/components/HeroBanner", () => ({
  default: () => <div data-kind="hero" />,
}));

vi.mock("@/components/SectionRow", () => ({
  default: ({ section }: { section: { id: string; items: Array<{ title: string }> } }) => (
    <div
      data-kind="section-row"
      data-section-id={section.id}
      data-first-item={section.items[0]?.title}
    />
  ),
}));

describe("Home", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);

    mockUseHomeLayout.mockReturnValue({
      data: { sections: [] },
      isLoading: false,
      isError: false,
      refetch: vi.fn(),
    });
    mockFetchHomeSectionItems.mockReset();
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

  it("does not invalidate cached home sections on mount", async () => {
    const invalidateQueries = vi.spyOn(QueryClient.prototype, "invalidateQueries");
    const queryClient = new QueryClient();

    await act(async () => {
      root.render(
        <QueryClientProvider client={queryClient}>
          <Home />
        </QueryClientProvider>,
      );
      await Promise.resolve();
    });

    expect(invalidateQueries).not.toHaveBeenCalled();
    invalidateQueries.mockRestore();
  });

  it("keeps section items cached past the client-wide gc time", async () => {
    mockUseHomeLayout.mockReturnValue({
      data: {
        sections: [
          {
            id: "recent",
            section_type: "recently_added",
            title: "Recently Added",
            featured: false,
            item_limit: 10,
            is_custom: false,
            customized: false,
          },
        ],
      },
      isLoading: false,
      isError: false,
      refetch: vi.fn(),
    });
    mockFetchHomeSectionItems.mockResolvedValue({
      section: { id: "recent", items: [], total_count: 0 },
    });
    // The client default is what evicts observer-less section entries today.
    const queryClient = new QueryClient({ defaultOptions: { queries: { gcTime: 10 * 60_000 } } });

    await act(async () => {
      root.render(
        <QueryClientProvider client={queryClient}>
          <Home />
        </QueryClientProvider>,
      );
      await Promise.resolve();
    });

    const sectionQuery = queryClient
      .getQueryCache()
      .find({ queryKey: sectionKeys.homeItems("recent") });

    expect(sectionQuery).toBeDefined();
    expect(sectionQuery?.gcTime).toBe(60 * 60 * 1000);
  });

  it("keeps cached card trees out of the first Home commit", () => {
    const layout = Array.from({ length: 5 }, (_, index) => homeLayout(`row-${index + 1}`));
    mockUseHomeLayout.mockReturnValue({
      data: { sections: layout },
      isLoading: false,
      isError: false,
      refetch: vi.fn(),
    });
    const queryClient = new QueryClient();
    layout.forEach((section) => {
      queryClient.setQueryData(sectionKeys.homeItems(section.id), homeSection(section.id));
    });

    const markup = renderToStaticMarkup(
      <QueryClientProvider client={queryClient}>
        <Home />
      </QueryClientProvider>,
    );

    expect(markup.match(/data-home-section-placeholder=/g)).toHaveLength(5);
    expect(markup).not.toContain('data-kind="section-row"');
  });

  it.each([
    ["WebKit", SAFARI_USER_AGENT],
    ["Firefox", FIREFOX_USER_AGENT],
  ])(
    "restores above-fold %s Home rows during the sidebar return and gates only lower rows",
    async (_browser, userAgent) => {
      vi.spyOn(window.navigator, "userAgent", "get").mockReturnValue(userAgent);
      vi.stubGlobal("matchMedia", (query: string) => ({
        matches: query === "(min-width: 64rem)",
        media: query,
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
      }));
      document.documentElement.dataset.sidebarVisualCollapsed = "true";
      const layout = [homeLayout("row-1"), homeLayout("row-2"), homeLayout("row-3")];
      mockUseHomeLayout.mockReturnValue({
        data: { sections: layout },
        isLoading: false,
        isError: false,
        refetch: vi.fn(),
      });
      const queryClient = new QueryClient();
      layout.forEach((section) => {
        queryClient.setQueryData(sectionKeys.homeItems(section.id), homeSection(section.id));
      });

      await act(async () => {
        root.render(
          <div className="sidebar-main-stage">
            <QueryClientProvider client={queryClient}>
              <Home />
            </QueryClientProvider>
          </div>,
        );
        await Promise.resolve();
      });

      expect(container.querySelectorAll('[data-kind="section-row"]')).toHaveLength(2);
      expect(container.querySelectorAll("[data-home-section-placeholder]")).toHaveLength(1);

      const stage = container.querySelector(".sidebar-main-stage");
      const unrelatedTransition = new TransitionEvent("transitionend", { propertyName: "opacity" });
      await act(async () => {
        stage?.dispatchEvent(unrelatedTransition);
        await Promise.resolve();
      });
      expect(container.querySelectorAll('[data-kind="section-row"]')).toHaveLength(2);

      const transformTransition = new TransitionEvent("transitionend", {
        propertyName: "transform",
      });
      await act(async () => {
        stage?.dispatchEvent(transformTransition);
        await Promise.resolve();
      });

      expect(container.querySelectorAll('[data-kind="section-row"]')).toHaveLength(3);
      expect(container.querySelectorAll("[data-home-section-placeholder]")).toHaveLength(0);
    },
  );

  it.each([
    ["WebKit", SAFARI_USER_AGENT],
    ["Firefox", FIREFOX_USER_AGENT],
  ])(
    "restores %s Home rows at the deadline if the transition is interrupted",
    async (_browser, userAgent) => {
      vi.useFakeTimers();
      vi.spyOn(window.navigator, "userAgent", "get").mockReturnValue(userAgent);
      vi.stubGlobal("matchMedia", (query: string) => ({
        matches: query === "(min-width: 64rem)",
        media: query,
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
      }));
      document.documentElement.dataset.sidebarVisualCollapsed = "true";
      const layout = [homeLayout("row-1"), homeLayout("row-2"), homeLayout("row-3")];
      mockUseHomeLayout.mockReturnValue({
        data: { sections: layout },
        isLoading: false,
        isError: false,
        refetch: vi.fn(),
      });
      const queryClient = new QueryClient();
      layout.forEach((section) => {
        queryClient.setQueryData(sectionKeys.homeItems(section.id), homeSection(section.id));
      });

      await act(async () => {
        root.render(
          <div className="sidebar-main-stage">
            <QueryClientProvider client={queryClient}>
              <Home />
            </QueryClientProvider>
          </div>,
        );
        await Promise.resolve();
      });

      expect(container.querySelectorAll('[data-kind="section-row"]')).toHaveLength(2);
      expect(container.querySelectorAll("[data-home-section-placeholder]")).toHaveLength(1);

      await act(async () => {
        vi.advanceTimersByTime(SIDEBAR_DETAILS_REVEAL_DEADLINE_MS - 1);
        await Promise.resolve();
      });
      expect(container.querySelectorAll('[data-kind="section-row"]')).toHaveLength(2);

      await act(async () => {
        vi.advanceTimersByTime(1);
        await Promise.resolve();
      });
      expect(container.querySelectorAll('[data-kind="section-row"]')).toHaveLength(3);
      expect(container.querySelectorAll("[data-home-section-placeholder]")).toHaveLength(0);
    },
  );

  it("does not gate Chromium Home rows during the sidebar return", async () => {
    vi.spyOn(window.navigator, "userAgent", "get").mockReturnValue(CHROME_USER_AGENT);
    vi.stubGlobal("matchMedia", (query: string) => ({
      matches: query === "(min-width: 64rem)",
      media: query,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    }));
    document.documentElement.dataset.sidebarVisualCollapsed = "true";
    const layout = [homeLayout("row-1")];
    mockUseHomeLayout.mockReturnValue({
      data: { sections: layout },
      isLoading: false,
      isError: false,
      refetch: vi.fn(),
    });
    const queryClient = new QueryClient();
    queryClient.setQueryData(sectionKeys.homeItems("row-1"), homeSection("row-1"));

    await act(async () => {
      root.render(
        <div className="sidebar-main-stage">
          <QueryClientProvider client={queryClient}>
            <Home />
          </QueryClientProvider>
        </div>,
      );
      await Promise.resolve();
    });

    expect(container.querySelectorAll('[data-kind="section-row"]')).toHaveLength(1);
    expect(container.querySelectorAll("[data-home-section-placeholder]")).toHaveLength(0);
  });

  it("mounts only nearby cached rows until lower Home sections approach the viewport", async () => {
    const observers: Array<{
      callback: IntersectionObserverCallback;
      targets: Set<Element>;
      disconnect: () => void;
      rootMargin?: string;
    }> = [];
    vi.stubGlobal(
      "IntersectionObserver",
      class {
        readonly root = null;
        readonly thresholds = [0];
        readonly rootMargin: string;
        private readonly record: (typeof observers)[number];

        constructor(callback: IntersectionObserverCallback, options?: IntersectionObserverInit) {
          this.rootMargin = options?.rootMargin ?? "0px";
          this.record = {
            callback,
            targets: new Set<Element>(),
            disconnect: vi.fn(),
            rootMargin: options?.rootMargin,
          };
          observers.push(this.record);
        }

        observe = (target: Element) => this.record.targets.add(target);
        unobserve = (target: Element) => this.record.targets.delete(target);
        disconnect = () => {
          this.record.disconnect();
          this.record.targets.clear();
        };
        takeRecords = () => [];
      },
    );

    const layout = Array.from({ length: 5 }, (_, index) => homeLayout(`row-${index + 1}`));
    mockUseHomeLayout.mockReturnValue({
      data: { sections: layout },
      isLoading: false,
      isError: false,
      refetch: vi.fn(),
    });
    const queryClient = new QueryClient();
    layout.forEach((section) => {
      queryClient.setQueryData(sectionKeys.homeItems(section.id), homeSection(section.id));
    });

    await act(async () => {
      root.render(
        <QueryClientProvider client={queryClient}>
          <Home />
        </QueryClientProvider>,
      );
      await Promise.resolve();
    });

    expect(container.querySelectorAll('[data-kind="section-row"]')).toHaveLength(2);
    expect(container.querySelectorAll("[data-home-section-placeholder]")).toHaveLength(3);
    expect(mockFetchHomeSectionItems).not.toHaveBeenCalled();
    expect(observers).toHaveLength(3);
    expect(observers.every((observer) => observer.rootMargin === "100% 0px")).toBe(true);

    const nextPlaceholder = container.querySelector('[data-home-section-placeholder="row-3"]');
    const nextObserver = observers.find(
      (observer) => nextPlaceholder && observer.targets.has(nextPlaceholder),
    );
    expect(nextObserver).toBeDefined();

    await act(async () => {
      nextObserver!.callback(
        [{ isIntersecting: true, target: nextPlaceholder } as IntersectionObserverEntry],
        {} as IntersectionObserver,
      );
      await Promise.resolve();
    });

    expect(container.querySelectorAll('[data-kind="section-row"]')).toHaveLength(3);
    expect(container.querySelectorAll("[data-home-section-placeholder]")).toHaveLength(2);
    expect(nextObserver!.disconnect).toHaveBeenCalled();

    await act(async () => root.unmount());
    expect(
      observers.every((observer) => vi.mocked(observer.disconnect).mock.calls.length > 0),
    ).toBe(true);
    root = createRoot(container);
  });

  it("counts only ready media rows toward the eager paint budget", async () => {
    const layout = [
      homeLayout("empty-1"),
      homeLayout("empty-2"),
      homeLayout("ready-1"),
      homeLayout("ready-2"),
    ];
    mockUseHomeLayout.mockReturnValue({
      data: { sections: layout },
      isLoading: false,
      isError: false,
      refetch: vi.fn(),
    });
    const queryClient = new QueryClient();
    for (const section of layout) {
      queryClient.setQueryData(
        sectionKeys.homeItems(section.id),
        section.id.startsWith("empty")
          ? { section: { ...section, total_count: 0, items: [] } }
          : homeSection(section.id),
      );
    }

    await act(async () => {
      root.render(
        <QueryClientProvider client={queryClient}>
          <Home />
        </QueryClientProvider>,
      );
      await Promise.resolve();
    });

    expect(container.querySelectorAll('[data-kind="section-row"]')).toHaveLength(2);
    expect(container.querySelectorAll("[data-home-section-placeholder]")).toHaveLength(0);
  });

  it("hard-caps restoration for large Home layouts", async () => {
    vi.useFakeTimers();
    vi.stubGlobal(
      "IntersectionObserver",
      class {
        readonly root = null;
        readonly thresholds = [0];
        readonly rootMargin = "100% 0px";
        constructor(_callback: IntersectionObserverCallback) {}
        observe = vi.fn();
        unobserve = vi.fn();
        disconnect = vi.fn();
        takeRecords = () => [];
      },
    );

    const layout = Array.from({ length: 20 }, (_, index) => homeLayout(`row-${index + 1}`));
    mockUseHomeLayout.mockReturnValue({
      data: { sections: layout },
      isLoading: false,
      isError: false,
      refetch: vi.fn(),
    });
    const queryClient = new QueryClient();
    layout.forEach((section) => {
      queryClient.setQueryData(sectionKeys.homeItems(section.id), homeSection(section.id));
    });

    await act(async () => {
      root.render(
        <QueryClientProvider client={queryClient}>
          <Home />
        </QueryClientProvider>,
      );
      await Promise.resolve();
    });

    expect(container.querySelectorAll('[data-kind="section-row"]')).toHaveLength(2);

    await act(async () => {
      vi.advanceTimersByTime(899);
      await Promise.resolve();
    });
    expect(container.querySelectorAll('[data-kind="section-row"]')).not.toHaveLength(20);

    await act(async () => {
      vi.advanceTimersByTime(1);
      await Promise.resolve();
    });

    expect(container.querySelectorAll('[data-kind="section-row"]')).toHaveLength(20);
    expect(container.querySelectorAll("[data-home-section-placeholder]")).toHaveLength(0);
  });

  it("renders every Home row in browsers without IntersectionObserver", async () => {
    vi.stubGlobal("IntersectionObserver", undefined);
    const layout = Array.from({ length: 5 }, (_, index) => homeLayout(`row-${index + 1}`));
    mockUseHomeLayout.mockReturnValue({
      data: { sections: layout },
      isLoading: false,
      isError: false,
      refetch: vi.fn(),
    });
    const queryClient = new QueryClient();
    layout.forEach((section) => {
      queryClient.setQueryData(sectionKeys.homeItems(section.id), homeSection(section.id));
    });

    await act(async () => {
      root.render(
        <QueryClientProvider client={queryClient}>
          <Home />
        </QueryClientProvider>,
      );
      await Promise.resolve();
    });

    expect(container.querySelectorAll('[data-kind="section-row"]')).toHaveLength(5);
    expect(container.querySelectorAll("[data-home-section-placeholder]")).toHaveLength(0);
  });

  it.each([
    ["WebKit", SAFARI_USER_AGENT],
    ["Firefox", FIREFOX_USER_AGENT],
  ])("renders every Home row immediately for reduced motion in %s", (_browser, userAgent) => {
    vi.spyOn(window.navigator, "userAgent", "get").mockReturnValue(userAgent);
    document.documentElement.dataset.sidebarVisualCollapsed = "true";
    vi.stubGlobal("matchMedia", (query: string) => ({
      matches: query === "(prefers-reduced-motion: reduce)",
      media: query,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    }));
    const layout = Array.from({ length: 5 }, (_, index) => homeLayout(`row-${index + 1}`));
    mockUseHomeLayout.mockReturnValue({
      data: { sections: layout },
      isLoading: false,
      isError: false,
      refetch: vi.fn(),
    });
    const queryClient = new QueryClient();
    layout.forEach((section) => {
      queryClient.setQueryData(sectionKeys.homeItems(section.id), homeSection(section.id));
    });

    const markup = renderToStaticMarkup(
      <QueryClientProvider client={queryClient}>
        <Home />
      </QueryClientProvider>,
    );

    expect(markup.match(/data-kind="section-row"/g)).toHaveLength(5);
    expect(markup).not.toContain("data-home-section-placeholder");
  });

  it("renders stale cached rows immediately while refreshing them in the background", async () => {
    const layout = [homeLayout("stale")];
    const cached = homeSection("stale");
    const refreshed = homeSection("stale");
    mockUseHomeLayout.mockReturnValue({
      data: { sections: layout },
      isLoading: false,
      isError: false,
      refetch: vi.fn(),
    });
    mockFetchHomeSectionItems.mockResolvedValue(refreshed);
    const queryClient = new QueryClient();
    queryClient.setQueryData(sectionKeys.homeItems("stale"), cached, {
      updatedAt: Date.now() - 11 * 60 * 1000,
    });

    await act(async () => {
      root.render(
        <QueryClientProvider client={queryClient}>
          <Home />
        </QueryClientProvider>,
      );
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(container.querySelector('[data-section-id="stale"]')).not.toBeNull();
    expect(mockFetchHomeSectionItems).toHaveBeenCalledWith(
      "stale",
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    );
  });

  describe("refresh bump during a section load", () => {
    const layout = [homeLayout("row-1"), homeLayout("row-2"), homeLayout("row-3")];

    beforeEach(() => {
      mockUseHomeLayout.mockReturnValue({
        data: { sections: layout },
        isLoading: false,
        isError: false,
        refetch: vi.fn(),
      });
    });

    it("keeps a cold load out of the error state and renders the newest data", async () => {
      const requests = deferSectionRequests();
      const queryClient = new QueryClient();

      await renderHome(queryClient);
      await waitForRequestCount(requests, 3);
      expect(requests.map((request) => request.sectionId)).toEqual(["row-1", "row-2", "row-3"]);

      bumpHomeRefreshSignal(queryClient);
      await waitForRequestCount(requests, 6);

      expect(sectionErrorCount()).toBe(0);
      const [firstGeneration, secondGeneration] = [requests.slice(0, 3), requests.slice(3)];
      expect(firstGeneration.every((request) => request.signal.aborted)).toBe(true);
      expect(secondGeneration.map((request) => request.sectionId)).toEqual([
        "row-1",
        "row-2",
        "row-3",
      ]);

      // The cancelled generation answers after the new one, so a stale result
      // that got through would overwrite the new data.
      secondGeneration.forEach((request) => request.resolve("after bump"));
      firstGeneration.forEach((request) => request.resolve("before bump"));
      await waitForRenderedFirstItems({
        "row-1": "row-1 after bump",
        "row-2": "row-2 after bump",
        "row-3": "row-3 after bump",
      });

      expect(sectionErrorCount()).toBe(0);
      expect(requests).toHaveLength(6);
    });

    it("refreshes stale cached rows that were in flight when the bump landed", async () => {
      const requests = deferSectionRequests();
      const queryClient = new QueryClient();
      layout.forEach((section) => {
        queryClient.setQueryData(
          sectionKeys.homeItems(section.id),
          homeSection(section.id, "cached"),
          { updatedAt: Date.now() - 11 * 60 * 1000 },
        );
      });

      await renderHome(queryClient);
      await waitForRequestCount(requests, 3);

      bumpHomeRefreshSignal(queryClient);
      await waitForRequestCount(requests, 6);

      expect(sectionErrorCount()).toBe(0);
      const secondGeneration = requests.slice(3);
      expect(secondGeneration.map((request) => request.sectionId)).toEqual([
        "row-1",
        "row-2",
        "row-3",
      ]);

      secondGeneration.forEach((request) => request.resolve("after bump"));
      await waitForRenderedFirstItems({
        "row-1": "row-1 after bump",
        "row-2": "row-2 after bump",
        "row-3": "row-3 after bump",
      });

      expect(sectionErrorCount()).toBe(0);
      expect(requests).toHaveLength(6);
    });

    it("renders the newest generation when two bumps land before any request settles", async () => {
      const requests = deferSectionRequests();
      const queryClient = new QueryClient();

      await renderHome(queryClient);
      await waitForRequestCount(requests, 3);
      for (const expectedRequests of [6, 9]) {
        bumpHomeRefreshSignal(queryClient);
        await waitForRequestCount(requests, expectedRequests);
        expect(sectionErrorCount()).toBe(0);
      }

      const [olderGenerations, newestGeneration] = [requests.slice(0, 6), requests.slice(6)];
      expect(olderGenerations.every((request) => request.signal.aborted)).toBe(true);
      expect(newestGeneration.map((request) => request.sectionId)).toEqual([
        "row-1",
        "row-2",
        "row-3",
      ]);

      // The older generations answer after the newest one, so a stale result
      // that got through would overwrite the newest data.
      newestGeneration.forEach((request) => request.resolve("newest"));
      olderGenerations.forEach((request) => request.resolve("older"));
      await waitForRenderedFirstItems({
        "row-1": "row-1 newest",
        "row-2": "row-2 newest",
        "row-3": "row-3 newest",
      });

      expect(sectionErrorCount()).toBe(0);
      expect(requests).toHaveLength(9);
    });

    it("still shows an error row when a section request really fails", async () => {
      const requests = deferSectionRequests();
      const queryClient = new QueryClient();

      await renderHome(queryClient);
      await waitForRequestCount(requests, 3);

      requests[0]!.reject(new Error("boom"));
      requests.slice(1).forEach((request) => request.resolve("loaded"));
      await waitFor(() => {
        expect(sectionErrorCount()).toBe(1);
        expect(renderedFirstItems()).toEqual({
          "row-2": "row-2 loaded",
          "row-3": "row-3 loaded",
        });
      });
    });
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

  function sectionErrorCount() {
    return (container.textContent?.match(/could not be loaded right now/g) ?? []).length;
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
          reject,
        });
      }),
  );
  return requests;
}

// Home hears about a refresh bump through a query observer that TanStack
// notifies on its own schedule. The next generation's requests are the
// observable sign that Home has reset and re-requested its sections.
async function waitForRequestCount(requests: DeferredSectionRequest[], count: number) {
  await waitFor(() => expect(requests).toHaveLength(count));
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

function homeSection(id: string, label = "item") {
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
