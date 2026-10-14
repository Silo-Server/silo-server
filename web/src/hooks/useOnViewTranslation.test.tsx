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

import { useOnViewTranslation } from "./useOnViewTranslation";

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
});
