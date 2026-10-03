import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { makePreparation, makePreparationList } from "@/test/downloadPreparations";
import type { AdminDownloadPreparationList } from "@/api/v2/adminDownloadPreparations";

const KEY = ["admin", "downloadPreparations", "p1"];

const mocks = vi.hoisted(() => ({
  list: vi.fn<() => Promise<AdminDownloadPreparationList>>(),
}));

vi.mock("@/api/client", () => ({
  captureProfileRequestContext: () => ({ profileId: "p1" }),
  StaleApiRequestContextError: class extends Error {},
}));
vi.mock("@/api/v2/adminDownloadPreparations", () => ({
  adminDownloadPreparationsKey: () => KEY,
  listAdminDownloadPreparations: mocks.list,
}));

import {
  ADMIN_DOWNLOAD_PREPARATION_FAILED_WINDOW,
  ADMIN_DOWNLOAD_PREPARATIONS_ACTIVE_REFRESH,
  nextFailureExpiry,
  useAdminDownloadPreparationsRefresh,
} from "./downloadPreparations";

function setup() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  function wrapper({ children }: { children: ReactNode }) {
    return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
  }
  renderHook(() => useAdminDownloadPreparationsRefresh(), { wrapper });
  return client;
}

describe("useAdminDownloadPreparationsRefresh", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    mocks.list.mockReset();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("re-reads the list on schedule even while progress patches keep arriving", async () => {
    const list = makePreparationList([makePreparation()]);
    mocks.list.mockResolvedValue(list);
    const client = setup();
    await act(async () => {});
    expect(mocks.list).toHaveBeenCalledTimes(1);

    // A progress event patches the cached list every five seconds.
    for (let elapsed = 0; elapsed < ADMIN_DOWNLOAD_PREPARATIONS_ACTIVE_REFRESH; elapsed += 5_000) {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(5_000);
        client.setQueryData<AdminDownloadPreparationList>(KEY, (current) =>
          current ? { ...current, items: [...current.items] } : current,
        );
      });
    }
    expect(mocks.list).toHaveBeenCalledTimes(2);
  });

  it("stops re-reading once nothing is in flight", async () => {
    mocks.list.mockResolvedValue(makePreparationList([], { failed_recent: 1 }));
    setup();
    await act(async () => {});
    await act(async () => {
      await vi.advanceTimersByTimeAsync(ADMIN_DOWNLOAD_PREPARATIONS_ACTIVE_REFRESH * 3);
    });
    expect(mocks.list).toHaveBeenCalledTimes(1);
  });

  it("re-reads once when the oldest listed failure ages out", async () => {
    const failedAt = new Date(Date.now() - ADMIN_DOWNLOAD_PREPARATION_FAILED_WINDOW + 10 * 60_000);
    const withFailure = makePreparationList([
      makePreparation({
        id: "f1",
        state: "failed",
        progress: undefined,
        failed_at: failedAt.toISOString(),
      }),
    ]);
    mocks.list.mockResolvedValueOnce(withFailure).mockResolvedValue(makePreparationList([]));
    setup();
    await act(async () => {});
    expect(mocks.list).toHaveBeenCalledTimes(1);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(9 * 60_000);
    });
    expect(mocks.list).toHaveBeenCalledTimes(1);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2 * 60_000);
    });
    expect(mocks.list).toHaveBeenCalledTimes(2);
    // The emptied list schedules nothing further.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(ADMIN_DOWNLOAD_PREPARATION_FAILED_WINDOW);
    });
    expect(mocks.list).toHaveBeenCalledTimes(2);
  });
});

describe("useAdminDownloadPreparationsRefresh with clock skew", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    mocks.list.mockReset();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("retries on a bounded cadence while an expired failure is still listed", async () => {
    // The server still lists a failure this browser already considers expired,
    // as when the browser clock runs ahead.
    const failedAt = new Date(Date.now() - ADMIN_DOWNLOAD_PREPARATION_FAILED_WINDOW - 2 * 60_000);
    const stale = makePreparationList([
      makePreparation({
        id: "f1",
        state: "failed",
        progress: undefined,
        failed_at: failedAt.toISOString(),
      }),
    ]);
    mocks.list
      .mockResolvedValueOnce(stale)
      .mockResolvedValueOnce(stale)
      .mockResolvedValue(makePreparationList([]));
    setup();
    await act(async () => {});
    expect(mocks.list).toHaveBeenCalledTimes(1);
    // No tight loop: nothing happens well inside the retry cadence.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(ADMIN_DOWNLOAD_PREPARATIONS_ACTIVE_REFRESH - 5_000);
    });
    expect(mocks.list).toHaveBeenCalledTimes(1);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5_000);
    });
    expect(mocks.list).toHaveBeenCalledTimes(2);
    // That read still listed the failure, so the timer re-arms (just after
    // the read lands) and a later read clears it.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(ADMIN_DOWNLOAD_PREPARATIONS_ACTIVE_REFRESH + 1_000);
    });
    expect(mocks.list).toHaveBeenCalledTimes(3);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(ADMIN_DOWNLOAD_PREPARATIONS_ACTIVE_REFRESH * 3);
    });
    expect(mocks.list).toHaveBeenCalledTimes(3);
  });

  it("re-arms the expiry read after a failed read", async () => {
    const failedAt = new Date(Date.now() - ADMIN_DOWNLOAD_PREPARATION_FAILED_WINDOW + 60_000);
    mocks.list
      .mockResolvedValueOnce(
        makePreparationList([
          makePreparation({
            id: "f1",
            state: "failed",
            progress: undefined,
            failed_at: failedAt.toISOString(),
          }),
        ]),
      )
      .mockRejectedValueOnce(new Error("offline"))
      .mockResolvedValue(makePreparationList([]));
    setup();
    await act(async () => {});
    expect(mocks.list).toHaveBeenCalledTimes(1);
    // The expiry read fails, as during an outage.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(61_000);
    });
    expect(mocks.list).toHaveBeenCalledTimes(2);
    // The failed read re-arms the timer, so a later read clears the failure.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(ADMIN_DOWNLOAD_PREPARATIONS_ACTIVE_REFRESH + 1_000);
    });
    expect(mocks.list).toHaveBeenCalledTimes(3);
  });
});

describe("nextFailureExpiry", () => {
  it("returns the earliest expiry among failed jobs", () => {
    const list = makePreparationList([
      makePreparation({ id: "r" }),
      makePreparation({ id: "a", state: "failed", failed_at: "2026-01-02T00:00:00.000Z" }),
      makePreparation({ id: "b", state: "failed", failed_at: "2026-01-01T00:00:00.000Z" }),
    ]);
    expect(nextFailureExpiry(list)).toBe(
      Date.parse("2026-01-01T00:00:00.000Z") + ADMIN_DOWNLOAD_PREPARATION_FAILED_WINDOW,
    );
    expect(nextFailureExpiry(makePreparationList([makePreparation()]))).toBeNull();
    expect(nextFailureExpiry(undefined)).toBeNull();
  });
});
