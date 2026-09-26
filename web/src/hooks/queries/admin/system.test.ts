import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ v2: vi.fn() }));
vi.mock("@/api/v2/request", () => ({ v2: mocks.v2 }));
import { useBuildInfo, useSystemResources } from "./system";

function wrapper({ children }: { children: ReactNode }) {
  return createElement(
    QueryClientProvider,
    {
      client: new QueryClient({ defaultOptions: { queries: { retry: false } } }),
    },
    children,
  );
}

describe("admin system v2 consumers", () => {
  it("adapts absent build timestamps for the existing presentation", async () => {
    mocks.v2.mockResolvedValueOnce({
      display: "unavailable",
      revision: "",
      dirty: false,
      build_number: 0,
      available: false,
    });
    const { result } = renderHook(() => useBuildInfo(), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data?.vcs_time).toBe("");
    expect(mocks.v2).toHaveBeenLastCalledWith("GET /api/v2/admin/system/build");
  });

  it("keeps unsampled resources distinct from a zero sample", async () => {
    mocks.v2
      .mockResolvedValueOnce({ state: "unavailable", instance_attribution: false })
      .mockResolvedValueOnce({ available: false, gpu: [] });
    const { result } = renderHook(() => useSystemResources(), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toEqual({ available: false, gpu: [] });
    expect(mocks.v2).toHaveBeenLastCalledWith("GET /api/v2/admin/system/resources");
  });
});
