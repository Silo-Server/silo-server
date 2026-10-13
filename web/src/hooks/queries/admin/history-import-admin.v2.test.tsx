// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook } from "@testing-library/react";
import { type ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { v2 } from "@/api/v2/request";
import { toast } from "sonner";
import { useCancelAdminRun, useCreateAdminRunForMapping } from "./history-import-admin";
vi.mock("@/api/v2/request", async (original) => ({
  ...(await original<typeof import("@/api/v2/request")>()),
  v2: vi.fn(),
}));
vi.mock("@/api/client", async (original) => ({
  ...(await original<typeof import("@/api/client")>()),
  captureProfileRequestContext: () => ({
    accessToken: "test",
    authContextVersion: 1,
    serverOrigin: "",
    profileId: "owner",
    profileToken: null,
  }),
  isProfileRequestContextCurrent: () => true,
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));
function wrapper() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: 3 } },
  });
  return ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
}
beforeEach(() => vi.useFakeTimers());
afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.clearAllMocks();
});
describe("durable admin import runs", () => {
  it("reports cancellation intent while the returned run remains running", async () => {
    vi.mocked(v2).mockResolvedValue({
      id: "run",
      user_id: "1",
      status: "canceling",
      terminal: false,
      cancelable: false,
    } as never);
    const { result } = renderHook(() => useCancelAdminRun(), { wrapper: wrapper() });
    await act(async () => {
      await result.current.mutateAsync("run");
    });
    expect(toast.success).toHaveBeenCalledWith("Cancellation requested");
    expect(v2).toHaveBeenCalledTimes(1);
  });
  it("never retries an ambiguous start despite client mutation retry defaults", async () => {
    vi.mocked(v2).mockRejectedValue(new Error("connection closed"));
    const { result } = renderHook(() => useCreateAdminRunForMapping(), { wrapper: wrapper() });
    await act(async () => {
      await expect(result.current.mutateAsync(2)).rejects.toThrow("connection closed");
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10000);
    });
    expect(v2).toHaveBeenCalledTimes(1);
  });
});
