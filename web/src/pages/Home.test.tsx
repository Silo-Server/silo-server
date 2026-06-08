// @vitest-environment jsdom

import { act } from "react";
import type { ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import Home from "./Home";

(
  globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean }
).IS_REACT_ACT_ENVIRONMENT = true;

const mockUseHomeLayout = vi.fn();

vi.mock("@/hooks/queries/sections", () => ({
  useHomeLayout: (...args: unknown[]) => mockUseHomeLayout(...args),
  fetchHomeSectionItems: vi.fn(),
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
});
