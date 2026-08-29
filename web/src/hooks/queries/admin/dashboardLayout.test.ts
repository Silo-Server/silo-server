import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { AdminDashboardLayoutResponse } from "@/api/types";

const mocks = vi.hoisted(() => ({
  api: vi.fn(),
  toastError: vi.fn(),
  toastSuccess: vi.fn(),
}));

vi.mock("@/api/client", () => ({
  api: mocks.api,
}));

vi.mock("sonner", () => ({
  toast: {
    error: mocks.toastError,
    success: mocks.toastSuccess,
  },
}));

import {
  useAdminDashboardLayout,
  useResetAdminDashboardLayout,
  useSaveAdminDashboardLayout,
} from "./dashboardLayout";

function createQueryClient() {
  return new QueryClient({
    defaultOptions: { mutations: { retry: false }, queries: { retry: false } },
  });
}

function createWrapper(queryClient: QueryClient) {
  return function Wrapper({ children }: { children: ReactNode }) {
    return createElement(QueryClientProvider, { client: queryClient }, children);
  };
}

const layoutDocument = {
  version: 1,
  entries: [{ id: "libraries", span: 7, rows: 4 }],
};

describe("useAdminDashboardLayout", () => {
  beforeEach(() => {
    mocks.api.mockReset();
    mocks.toastError.mockReset();
    mocks.toastSuccess.mockReset();
  });

  it("reads the saved layout for the current admin", async () => {
    const response: AdminDashboardLayoutResponse = {
      layout: layoutDocument,
      updated_at: "2026-08-26T10:00:00Z",
    };
    mocks.api.mockResolvedValue(response);
    const { result } = renderHook(() => useAdminDashboardLayout(), {
      wrapper: createWrapper(createQueryClient()),
    });

    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    expect(mocks.api).toHaveBeenCalledWith("/admin/dashboard/layout");
    expect(result.current.data).toEqual(response);
  });

  it("reports a never-saved layout as null rather than an error", async () => {
    mocks.api.mockResolvedValue({ layout: null, updated_at: null });
    const { result } = renderHook(() => useAdminDashboardLayout(), {
      wrapper: createWrapper(createQueryClient()),
    });

    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    expect(result.current.data).toEqual({ layout: null, updated_at: null });
  });
});

describe("useSaveAdminDashboardLayout", () => {
  beforeEach(() => {
    mocks.api.mockReset();
    mocks.toastError.mockReset();
    mocks.toastSuccess.mockReset();
  });

  it("PUTs the layout document and seeds the query cache", async () => {
    const queryClient = createQueryClient();
    mocks.api.mockResolvedValue(undefined);
    const { result } = renderHook(() => useSaveAdminDashboardLayout(), {
      wrapper: createWrapper(queryClient),
    });

    await act(async () => {
      await result.current.mutateAsync(layoutDocument);
    });

    expect(mocks.api).toHaveBeenCalledWith("/admin/dashboard/layout", {
      method: "PUT",
      body: JSON.stringify({ layout: layoutDocument }),
    });
    const cached = queryClient.getQueryData<AdminDashboardLayoutResponse>([
      "admin",
      "dashboard",
      "layout",
    ]);
    expect(cached?.layout).toEqual(layoutDocument);
    expect(cached?.updated_at).toEqual(expect.any(String));
    expect(mocks.toastError).not.toHaveBeenCalled();
  });

  it("surfaces a save failure once without discarding local state", async () => {
    const queryClient = createQueryClient();
    mocks.api.mockRejectedValue(new Error("offline"));
    const { result } = renderHook(() => useSaveAdminDashboardLayout(), {
      wrapper: createWrapper(queryClient),
    });

    await act(async () => {
      try {
        await result.current.mutateAsync(layoutDocument);
      } catch {
        // The hook reports the failure through a toast; local state is kept.
      }
    });

    expect(mocks.toastError).toHaveBeenCalledTimes(1);
    expect(queryClient.getQueryData(["admin", "dashboard", "layout"])).toBeUndefined();
  });
});

// Both mutations share one scope, so react-query runs them one at a time in
// the order they were started. Without that an older PUT can land last and
// seed the cache with a stale document, and a reset can be overtaken by a save
// that resurrects the arrangement it discarded.
describe("dashboard layout writes are serialized", () => {
  beforeEach(() => {
    mocks.api.mockReset();
    mocks.toastError.mockReset();
    mocks.toastSuccess.mockReset();
  });

  function renderWriters(queryClient: QueryClient) {
    return renderHook(
      () => ({
        save: useSaveAdminDashboardLayout(),
        reset: useResetAdminDashboardLayout(),
      }),
      { wrapper: createWrapper(queryClient) },
    );
  }

  it("holds a second save until the first one finishes", async () => {
    const queryClient = createQueryClient();
    const started: string[] = [];
    const release: (() => void)[] = [];
    mocks.api.mockImplementation(() => {
      started.push("PUT");
      return new Promise<void>((resolve) => release.push(() => resolve()));
    });
    const { result } = renderWriters(queryClient);

    const second = { version: 1, entries: [{ id: "users", span: 5, rows: 4 }] };
    act(() => {
      result.current.save.mutate(layoutDocument);
      result.current.save.mutate(second);
    });

    await waitFor(() => expect(started).toEqual(["PUT"]));
    act(() => release[0]?.());
    await waitFor(() => expect(started).toEqual(["PUT", "PUT"]));
    act(() => release[1]?.());

    await waitFor(() =>
      expect(
        queryClient.getQueryData<AdminDashboardLayoutResponse>(["admin", "dashboard", "layout"])
          ?.layout,
      ).toEqual(second),
    );
  });

  it("runs a reset after a save that is already in flight", async () => {
    const queryClient = createQueryClient();
    const started: string[] = [];
    let releaseSave: (() => void) | undefined;
    mocks.api.mockImplementation((_path: string, init?: { method?: string }) => {
      started.push(init?.method ?? "GET");
      if (init?.method === "PUT") {
        return new Promise<void>((resolve) => {
          releaseSave = () => resolve();
        });
      }
      return Promise.resolve(undefined);
    });
    const { result } = renderWriters(queryClient);

    act(() => {
      result.current.save.mutate(layoutDocument);
      result.current.reset.mutate();
    });

    await waitFor(() => expect(started).toEqual(["PUT"]));
    act(() => releaseSave?.());

    await waitFor(() => expect(started).toEqual(["PUT", "DELETE"]));
    await waitFor(() =>
      expect(queryClient.getQueryData(["admin", "dashboard", "layout"])).toEqual({
        layout: null,
        updated_at: null,
      }),
    );
  });
});

describe("useResetAdminDashboardLayout", () => {
  beforeEach(() => {
    mocks.api.mockReset();
    mocks.toastError.mockReset();
    mocks.toastSuccess.mockReset();
  });

  it("DELETEs the layout and clears the cached document", async () => {
    const queryClient = createQueryClient();
    queryClient.setQueryData<AdminDashboardLayoutResponse>(["admin", "dashboard", "layout"], {
      layout: layoutDocument,
      updated_at: "2026-08-26T10:00:00Z",
    });
    mocks.api.mockResolvedValue(undefined);
    const { result } = renderHook(() => useResetAdminDashboardLayout(), {
      wrapper: createWrapper(queryClient),
    });

    await act(async () => {
      await result.current.mutateAsync();
    });

    expect(mocks.api).toHaveBeenCalledWith("/admin/dashboard/layout", { method: "DELETE" });
    expect(queryClient.getQueryData(["admin", "dashboard", "layout"])).toEqual({
      layout: null,
      updated_at: null,
    });
  });
});
