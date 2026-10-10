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

  it("keeps polling for an earlier hero slide after the next one starts", async () => {
    vi.useFakeTimers();
    try {
      const onPoll = vi.fn();
      const { result, rerender } = renderHook(
        ({ item }) => useOnViewTranslation(item, { onPoll }),
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
});
