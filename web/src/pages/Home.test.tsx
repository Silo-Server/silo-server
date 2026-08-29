// @vitest-environment jsdom

import { act } from "react";
import type { ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import Home from "./Home";
import { sectionKeys } from "@/hooks/queries/keys";

(
  globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean }
).IS_REACT_ACT_ENVIRONMENT = true;

const mockUseHomeLayout = vi.fn();
const mockFetchHomeSectionItems = vi.fn();

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
  default: () => <div data-kind="section-row" />,
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
});
