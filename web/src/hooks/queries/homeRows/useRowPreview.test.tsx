import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { useRowPreview } from "./useRowPreview";

const mocks = vi.hoisted(() => ({ fetchRowPreview: vi.fn() }));

vi.mock("@/lib/homeRows/peek", () => ({ fetchRowPreview: mocks.fetchRowPreview }));

type Draft = { sectionType: string; config: Record<string, unknown> };

function wrapper({ children }: { children: ReactNode }) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

beforeEach(() => {
  mocks.fetchRowPreview.mockReset();
});

describe("useRowPreview", () => {
  it("marks the last preview as refreshing until the changed draft's preview arrives", async () => {
    let release = () => {};
    mocks.fetchRowPreview.mockImplementation(async (draft: Draft) => {
      const year = draft.config.year as number;
      if (year === 2000) await new Promise<void>((resolve) => (release = resolve));
      return { items: [], totalCount: year === 1999 ? 12 : 3 };
    });
    const { result, rerender } = renderHook(
      ({ draft }: { draft: Draft }) => useRowPreview(draft, { kind: "home" }, true),
      { wrapper, initialProps: { draft: { sectionType: "query", config: { year: 1999 } } } },
    );
    await waitFor(() =>
      expect(result.current).toEqual({
        status: "ready",
        items: [],
        totalCount: 12,
        refreshing: false,
      }),
    );

    rerender({ draft: { sectionType: "query", config: { year: 2000 } } });
    // During the debounce and the next request, the old count stays but is marked stale.
    expect(result.current).toMatchObject({ status: "ready", totalCount: 12, refreshing: true });
    await waitFor(() => expect(mocks.fetchRowPreview).toHaveBeenCalledTimes(2));
    expect(result.current).toMatchObject({ totalCount: 12, refreshing: true });

    release();
    await waitFor(() => expect(result.current).toMatchObject({ totalCount: 3, refreshing: false }));
  });
});
