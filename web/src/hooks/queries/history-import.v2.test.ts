// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { isCapturedProfileAuthorityActive } from "@/api/client";
import { v2 } from "@/api/v2/request";
import {
  useCheckPlexPin,
  useCreateHistoryImportRun,
  useHistoryImportRuns,
  useHistoryImportRun,
} from "./history-import";

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
  isCapturedProfileAuthorityActive: vi.fn(() => true),
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

function wrapper() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: 3 } },
  });
  return ({ children }: { children: ReactNode }) =>
    createElement(QueryClientProvider, { client }, children);
}

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.clearAllMocks();
});

describe("history import v2 hooks", () => {
  it("reads the bounded runs envelope and adapts opaque account identifiers", async () => {
    vi.mocked(v2).mockResolvedValueOnce({
      items: [{ id: "run", user_id: "42", mapping_id: "3", status: "completed" }],
      page: { has_more: false },
    } as never);
    const { result } = renderHook(() => useHistoryImportRuns(10), { wrapper: wrapper() });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(v2).toHaveBeenCalledWith(
      "GET /api/v2/history-imports/runs",
      expect.objectContaining({ query: { limit: 10 } }),
    );
    expect(result.current.data?.[0]).toMatchObject({ id: "run", user_id: 42, mapping_id: 3 });
  });

  it("starts a run with its target profile and string source identifier", async () => {
    vi.mocked(v2).mockImplementationOnce((_op, options) => {
      (options as { onResponse?: (r: Response) => void }).onResponse?.(
        new Response(null, {
          headers: { Location: "/api/v2/history-imports/runs/new", "Retry-After": "2" },
        }),
      );
      return Promise.resolve({
        id: "new",
        user_id: "42",
        profile_id: "target",
        status: "queued",
        terminal: false,
        cancelable: false,
      }) as never;
    });
    const { result } = renderHook(() => useCreateHistoryImportRun(), { wrapper: wrapper() });
    await act(async () => {
      await result.current.mutateAsync({ profile_id: "target", source: "plex", source_id: 9 });
    });
    expect(v2).toHaveBeenCalledWith(
      "POST /api/v2/history-imports/runs",
      expect.objectContaining({ body: { profile_id: "target", source: "plex", source_id: "9" } }),
    );
  });

  it("surfaces an ambiguous Plex exchange error without a retry", async () => {
    vi.mocked(v2).mockRejectedValueOnce(new Error("connection closed"));
    const { result } = renderHook(() => useCheckPlexPin("session"), { wrapper: wrapper() });
    await waitFor(() => expect(result.current.isError).toBe(true));
    expect(result.current.failureCount).toBe(1);
    expect(v2).toHaveBeenCalledTimes(1);
  });
});

describe("durable personal import monitor", () => {
  it("honors polling interval and stops after terminal cancellation", async () => {
    vi.useFakeTimers();
    let reads = 0;
    vi.mocked(v2).mockImplementation((_op, options) => {
      (options as { onResponse?: (r: Response) => void }).onResponse?.(
        new Response(null, {
          headers: { "Retry-After": "2", Location: "/api/v2/history-imports/runs/run" },
        }),
      );
      return Promise.resolve({
        id: "run",
        user_id: "1",
        status: ++reads === 1 ? "canceling" : "cancelled",
        terminal: reads > 1,
        cancelable: false,
      }) as never;
    });
    renderHook(() => useHistoryImportRun("run"), { wrapper: wrapper() });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1);
    });
    expect(reads).toBe(1);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1998);
    });
    expect(reads).toBe(1);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2);
    });
    expect(reads).toBe(2);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10000);
    });
    expect(reads).toBe(2);
  });
  it("does not retry uncertain acceptance despite mutation retry defaults", async () => {
    vi.mocked(v2).mockRejectedValue(
      new Error("Acceptance could not be confirmed; check imports before submitting again"),
    );
    const { result } = renderHook(() => useCreateHistoryImportRun(), { wrapper: wrapper() });
    await act(async () => {
      await expect(result.current.mutateAsync({ profile_id: "p", source: "plex" })).rejects.toThrow(
        "acceptance could not be confirmed",
      );
    });
    expect(v2).toHaveBeenCalledTimes(1);
  });
  it("refuses an unexpected accepted Location without resubmitting", async () => {
    vi.mocked(v2).mockImplementation((_op, options) => {
      (options as { onResponse?: (r: Response) => void }).onResponse?.(
        new Response(null, { headers: { Location: "https://unexpected.invalid/run" } }),
      );
      return Promise.resolve({
        id: "run",
        user_id: "1",
        status: "queued",
        terminal: false,
        cancelable: false,
      }) as never;
    });
    const { result } = renderHook(() => useCreateHistoryImportRun(), { wrapper: wrapper() });
    await act(async () => {
      await expect(result.current.mutateAsync({ profile_id: "p", source: "plex" })).rejects.toThrow(
        "Refresh imports",
      );
    });
    expect(v2).toHaveBeenCalledTimes(1);
  });
  it("stops failed polling until an explicit refresh", async () => {
    vi.useFakeTimers();
    vi.mocked(v2).mockRejectedValue(new Error("unavailable"));
    const { result } = renderHook(() => useHistoryImportRun("run"), { wrapper: wrapper() });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10000);
    });
    expect(v2).toHaveBeenCalledTimes(1);
    await act(async () => {
      await result.current.refetch();
    });
    expect(v2).toHaveBeenCalledTimes(2);
  });
});

it("does not publish an accepted run after profile authority is lost", async () => {
  vi.mocked(v2).mockImplementationOnce((_op, options) => {
    (options as { onResponse?: (r: Response) => void }).onResponse?.(
      new Response(null, { headers: { Location: "/api/v2/history-imports/runs/run" } }),
    );
    vi.mocked(isCapturedProfileAuthorityActive).mockReturnValueOnce(false);
    return Promise.resolve({
      id: "run",
      user_id: "1",
      status: "queued",
      terminal: false,
      cancelable: false,
    }) as never;
  });
  const { result } = renderHook(() => useCreateHistoryImportRun(), { wrapper: wrapper() });
  await act(async () => {
    await expect(result.current.mutateAsync({ profile_id: "p", source: "plex" })).rejects.toThrow();
  });
  expect(result.current.data).toBeUndefined();
  expect(v2).toHaveBeenCalledTimes(1);
});
