import { renderHook } from "@testing-library/react";
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
});
