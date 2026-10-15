import { act, renderHook } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  v2: vi.fn(() => Promise.resolve({})),
  mode: "auto",
}));

vi.mock("@/api/v2/request", () => ({
  v2: (...args: unknown[]) => mocks.v2(...(args as [])),
}));

vi.mock("@/hooks/queries/metadataAI", () => ({
  useMetadataAIStatus: () => ({ data: { enabled: true, on_view: mocks.mode } }),
}));

import { type OnViewTranslationTarget, useOnViewTranslation } from "./useOnViewTranslation";

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={new QueryClient()}>{children}</QueryClientProvider>;
}

describe("useOnViewTranslation", () => {
  afterEach(() => {
    vi.useRealTimers();
    mocks.v2.mockClear();
    mocks.mode = "auto";
  });

  it("translates a season from its episode list in auto mode", () => {
    renderHook(
      () => useOnViewTranslation({ content_id: "season-1", pending_translation_language: "de" }),
      { wrapper },
    );
    expect(mocks.v2).toHaveBeenCalledTimes(1);
    expect(mocks.v2).toHaveBeenCalledWith("POST /api/v2/catalog/items/{id}/translate-description", {
      path: { id: "season-1" },
      body: { target_language: "de" },
    });
  });

  it("does nothing when nothing is pending or the mode is not auto", () => {
    renderHook(() => useOnViewTranslation({ content_id: "season-1" }), { wrapper });
    mocks.mode = "button";
    const { result } = renderHook(
      () => useOnViewTranslation({ content_id: "season-2", pending_translation_language: "de" }),
      { wrapper },
    );
    expect(mocks.v2).not.toHaveBeenCalled();
    expect(result.current.onTranslate).toBeTypeOf("function");
  });

  it("lets the button retry after a failed request", async () => {
    mocks.mode = "button";
    mocks.v2.mockImplementationOnce(() => Promise.reject(new Error("offline")));
    const { result } = renderHook(
      () => useOnViewTranslation({ content_id: "movie-1", pending_translation_language: "de" }),
      { wrapper },
    );
    await act(async () => result.current.onTranslate?.());
    expect(result.current.translating).toBe(false);
    expect(result.current.onTranslate).toBeTypeOf("function");
    act(() => result.current.onTranslate?.());
    expect(mocks.v2).toHaveBeenCalledTimes(2);
    expect(result.current.translating).toBe(true);
  });

  it("offers a working retry once a long season job outlasts the wait", () => {
    vi.useFakeTimers();
    mocks.mode = "button";
    const { result } = renderHook(
      () => useOnViewTranslation({ content_id: "season-1", pending_translation_language: "de" }),
      { wrapper },
    );
    act(() => result.current.onTranslate?.());
    expect(mocks.v2).toHaveBeenCalledTimes(1);
    expect(result.current.translating).toBe(true);

    // The episodes are still pending when the wait runs out.
    act(() => vi.advanceTimersByTime(50_000));
    expect(result.current.translating).toBe(false);

    act(() => result.current.onTranslate?.());
    expect(mocks.v2).toHaveBeenCalledTimes(2);
    expect(result.current.translating).toBe(true);
  });

  it("keeps polling for an earlier hero slide after the next one starts", async () => {
    vi.useFakeTimers();
    try {
      const onPoll = vi.fn();
      // The hero passes the slides still pending, so its earlier jobs stay.
      const heroSlides: ReadonlySet<string> = new Set(["a", "b"]);
      const { result, rerender } = renderHook(
        ({ item }) => useOnViewTranslation(item, { onPoll, pendingContentIds: heroSlides }),
        {
          wrapper,
          initialProps: { item: { content_id: "a", pending_translation_language: "de" } },
        },
      );
      rerender({ item: { content_id: "b", pending_translation_language: "de" } });
      // Slide b finishes first; slide a's job is still running.
      rerender({ item: { content_id: "b", pending_translation_language: "" } });
      expect(result.current.translating).toBe(false);
      await act(() => vi.advanceTimersByTimeAsync(2_000));
      expect(onPoll).toHaveBeenCalledTimes(1);
      rerender({ item: { content_id: "a", pending_translation_language: "de" } });
      expect(result.current.translating).toBe(true);
      rerender({ item: { content_id: "a", pending_translation_language: "" } });
      await act(() => vi.advanceTimersByTimeAsync(4_000));
      expect(onPoll).toHaveBeenCalledTimes(1);
    } finally {
      vi.useRealTimers();
    }
  });

  it("stops polling when an earlier slide finishes while another one shows", async () => {
    vi.useFakeTimers();
    try {
      const onPoll = vi.fn();
      const { rerender } = renderHook(
        ({ item, pendingContentIds }) => useOnViewTranslation(item, { onPoll, pendingContentIds }),
        {
          wrapper,
          initialProps: {
            item: { content_id: "a", pending_translation_language: "de" },
            pendingContentIds: new Set(["a"]) as ReadonlySet<string>,
          },
        },
      );
      // The hero moves on to b, which is already localized.
      rerender({
        item: { content_id: "b", pending_translation_language: "" },
        pendingContentIds: new Set(["a"]),
      });
      await act(() => vi.advanceTimersByTimeAsync(2_000));
      expect(onPoll).toHaveBeenCalledTimes(1);
      // The refreshed section no longer reports a as pending: a is done.
      rerender({
        item: { content_id: "b", pending_translation_language: "" },
        pendingContentIds: new Set(),
      });
      await act(() => vi.advanceTimersByTimeAsync(10_000));
      expect(onPoll).toHaveBeenCalledTimes(1);
    } finally {
      vi.useRealTimers();
    }
  });

  it("keeps a retry going when the request it replaced fails late", async () => {
    vi.useFakeTimers();
    mocks.mode = "button";
    let rejectFirst: (reason: unknown) => void = () => {};
    mocks.v2.mockImplementationOnce(
      () =>
        new Promise((_, reject) => {
          rejectFirst = reject;
        }),
    );
    mocks.v2.mockImplementationOnce(() => new Promise(() => {}));
    const { result } = renderHook(
      () => useOnViewTranslation({ content_id: "season-1", pending_translation_language: "de" }),
      { wrapper },
    );
    act(() => result.current.onTranslate?.());
    act(() => vi.advanceTimersByTime(50_000));
    act(() => result.current.onTranslate?.());
    expect(mocks.v2).toHaveBeenCalledTimes(2);

    // The first, replaced request fails only now.
    await act(async () => {
      rejectFirst(new Error("network"));
      await Promise.resolve();
    });
    expect(result.current.translating).toBe(true);
  });

  it("keeps refreshing a long season job slowly after the wait in auto mode", () => {
    vi.useFakeTimers();
    const client = new QueryClient();
    const invalidate = vi.spyOn(client, "invalidateQueries");
    renderHook(
      () => useOnViewTranslation({ content_id: "season-1", pending_translation_language: "de" }),
      {
        wrapper: ({ children }: { children: ReactNode }) => (
          <QueryClientProvider client={client}>{children}</QueryClientProvider>
        ),
      },
    );
    expect(mocks.v2).toHaveBeenCalledTimes(1);
    act(() => vi.advanceTimersByTime(50_000));
    const afterWait = invalidate.mock.calls.length;

    // Episodes still pending: later batches keep reaching the page.
    act(() => vi.advanceTimersByTime(30_000));
    expect(invalidate.mock.calls.length).toBeGreaterThan(afterWait);
    expect(mocks.v2).toHaveBeenCalledTimes(1);
  });

  it("stops the slow refresh after its limit", () => {
    vi.useFakeTimers();
    const client = new QueryClient();
    const invalidate = vi.spyOn(client, "invalidateQueries");
    renderHook(
      () => useOnViewTranslation({ content_id: "season-1", pending_translation_language: "de" }),
      {
        wrapper: ({ children }: { children: ReactNode }) => (
          <QueryClientProvider client={client}>{children}</QueryClientProvider>
        ),
      },
    );
    // The 45-second wait, then the five-minute slow refresh.
    act(() => vi.advanceTimersByTime(45_000 + 5 * 60_000 + 20_000));
    const atLimit = invalidate.mock.calls.length;
    act(() => vi.advanceTimersByTime(60_000));
    expect(invalidate.mock.calls.length).toBe(atLimit);
  });

  it("stops a detail page's job when the page moves to another item", () => {
    vi.useFakeTimers();
    const client = new QueryClient();
    const invalidate = vi.spyOn(client, "invalidateQueries");
    const { rerender } = renderHook(({ item }) => useOnViewTranslation(item), {
      wrapper: ({ children }: { children: ReactNode }) => (
        <QueryClientProvider client={client}>{children}</QueryClientProvider>
      ),
      initialProps: {
        item: {
          content_id: "episode-a",
          pending_translation_language: "de",
          series_id: "show",
          season_number: 1,
        } as OnViewTranslationTarget,
      },
    });
    // The route moves on to an episode that is already localized.
    rerender({ item: { content_id: "episode-b", series_id: "show", season_number: 2 } });
    const afterMove = invalidate.mock.calls.length;
    act(() => vi.advanceTimersByTime(30_000));
    expect(invalidate.mock.calls.length).toBe(afterMove);
  });

  it("refreshes each running job's own season, not the item on screen", () => {
    vi.useFakeTimers();
    const client = new QueryClient();
    const invalidate = vi.spyOn(client, "invalidateQueries");
    const { rerender } = renderHook(
      ({ item, pendingContentIds }) => useOnViewTranslation(item, { pendingContentIds }),
      {
        wrapper: ({ children }: { children: ReactNode }) => (
          <QueryClientProvider client={client}>{children}</QueryClientProvider>
        ),
        initialProps: {
          item: {
            content_id: "episode-a",
            pending_translation_language: "de",
            series_id: "show",
            season_number: 1,
          } as OnViewTranslationTarget | undefined,
          pendingContentIds: new Set(["episode-a"]) as ReadonlySet<string>,
        },
      },
    );
    // The hero moves on to a movie slide while episode a still translates.
    rerender({ item: { content_id: "movie-b" }, pendingContentIds: new Set(["episode-a"]) });
    invalidate.mockClear();
    act(() => vi.advanceTimersByTime(2_000));
    expect(invalidate).toHaveBeenCalledWith({
      queryKey: ["catalog", "series", "show", "seasons", 1],
    });
  });

  it("marks cached Home and library rows stale when a translation lands", () => {
    const client = new QueryClient();
    const homeKey = ["sections", "home", "items", "featured"];
    const libraryKey = ["sections", "library", 7, "items", "featured"];
    client.setQueryData(homeKey, { section: { items: [] } });
    client.setQueryData(libraryKey, { section: { items: [] } });
    const { rerender } = renderHook(({ item }) => useOnViewTranslation(item), {
      wrapper: ({ children }: { children: ReactNode }) => (
        <QueryClientProvider client={client}>{children}</QueryClientProvider>
      ),
      initialProps: {
        item: {
          content_id: "movie-1",
          pending_translation_language: "de",
        } as OnViewTranslationTarget,
      },
    });
    expect(client.getQueryState(homeKey)?.isInvalidated).toBe(false);
    // The refetched detail comes back localized.
    rerender({ item: { content_id: "movie-1" } });
    expect(client.getQueryState(homeKey)?.isInvalidated).toBe(true);
    expect(client.getQueryState(libraryKey)?.isInvalidated).toBe(true);
  });

  it("marks cached rows stale when an off-screen hero slide's translation lands", () => {
    const client = new QueryClient();
    const homeKey = ["sections", "home", "items", "recent"];
    client.setQueryData(homeKey, { section: { items: [] } });
    const { rerender } = renderHook(
      ({ item, pendingContentIds }) => useOnViewTranslation(item, { pendingContentIds }),
      {
        wrapper: ({ children }: { children: ReactNode }) => (
          <QueryClientProvider client={client}>{children}</QueryClientProvider>
        ),
        initialProps: {
          item: { content_id: "a", pending_translation_language: "de" } as OnViewTranslationTarget,
          pendingContentIds: new Set(["a"]) as ReadonlySet<string>,
        },
      },
    );
    // The hero shows b while a is still translating.
    rerender({ item: { content_id: "b" }, pendingContentIds: new Set(["a"]) });
    expect(client.getQueryState(homeKey)?.isInvalidated).toBe(false);
    // The hero's refreshed section no longer reports a as pending.
    rerender({ item: { content_id: "b" }, pendingContentIds: new Set() });
    expect(client.getQueryState(homeKey)?.isInvalidated).toBe(true);
  });

  // Another tab or profile can finish the translation first, and the server
  // then refuses the request: the page re-reads its detail.
  it("re-reads the detail after a refused request", async () => {
    mocks.v2.mockImplementationOnce(() => Promise.reject(new Error("nothing to translate")));
    const queryClient = new QueryClient();
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");
    const { result } = renderHook(
      () => useOnViewTranslation({ content_id: "movie-1", pending_translation_language: "de" }),
      {
        wrapper: ({ children }: { children: ReactNode }) => (
          <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
        ),
      },
    );
    await act(async () => {});
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["catalog", "items", "movie-1"] });
    expect(result.current.translating).toBe(false);
  });

  it("lets the button try again after a failed request", async () => {
    mocks.mode = "button";
    mocks.v2.mockImplementationOnce(() => Promise.reject(new Error("rate limited")));
    const { result } = renderHook(
      () => useOnViewTranslation({ content_id: "movie-1", pending_translation_language: "de" }),
      { wrapper },
    );
    await act(async () => result.current.onTranslate?.());
    expect(result.current.translating).toBe(false);
    act(() => result.current.onTranslate?.());
    expect(mocks.v2).toHaveBeenCalledTimes(2);
  });

  // Auto mode fires once per view; after a failure such as a 429 it offers
  // the translate action so the viewer can try again without a reload.
  it("offers the translate action after a failed auto run", async () => {
    mocks.v2.mockImplementationOnce(() => Promise.reject(new Error("rate limited")));
    const { result } = renderHook(
      () => useOnViewTranslation({ content_id: "movie-1", pending_translation_language: "de" }),
      { wrapper },
    );
    expect(result.current.onTranslate).toBeUndefined();
    await act(async () => {});
    expect(mocks.v2).toHaveBeenCalledTimes(1);
    expect(result.current.onTranslate).toBeTypeOf("function");
    act(() => result.current.onTranslate?.());
    expect(mocks.v2).toHaveBeenCalledTimes(2);
    expect(result.current.onTranslate).toBeUndefined();
  });

  it("restarts a detail page's job when the page returns to its item", () => {
    const { rerender } = renderHook(({ item }) => useOnViewTranslation(item), {
      wrapper,
      initialProps: {
        item: {
          content_id: "movie-a",
          pending_translation_language: "de",
        } as OnViewTranslationTarget,
      },
    });
    expect(mocks.v2).toHaveBeenCalledTimes(1);
    // The route moves on to a cached item, then back to a.
    rerender({ item: { content_id: "movie-b" } });
    rerender({ item: { content_id: "movie-a", pending_translation_language: "de" } });
    expect(mocks.v2).toHaveBeenCalledTimes(2);
  });
});
