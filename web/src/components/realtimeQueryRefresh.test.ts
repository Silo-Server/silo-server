import { QueryClient, QueryObserver } from "@tanstack/react-query";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  setAccessToken,
  setProfileId,
  setProfileToken,
} from "@/api/client";
import { adminStatsKey } from "@/hooks/queries/admin/stats";
import { adminSessionsKey } from "@/api/v2/adminSessionsCache";
import { createRealtimeQueryRefreshScheduler } from "./realtimeQueryRefresh";

beforeEach(() => {
  vi.useFakeTimers();
  setAccessToken("admin");
  setProfileId("primary");
  setProfileToken(null);
});
afterEach(() => vi.useRealTimers());

function observeSessions(load: () => Promise<string[]>) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const authority = captureProfileRequestContext()!;
  const key = adminSessionsKey(authority);
  client.setQueryData(key, ["existing"]);
  const observer = new QueryObserver(client, { queryKey: key, queryFn: load, staleTime: Infinity });
  const unsubscribe = observer.subscribe(() => {});
  let allowUpdates = true;
  const refresh = createRealtimeQueryRefreshScheduler(
    client,
    () => isCapturedProfileAuthorityActive(authority),
    () => allowUpdates,
  );
  const scheduler = {
    schedule: () =>
      refresh.schedule(
        { queryKey: key, exact: true },
        { queryKey: adminStatsKey(authority), exact: true },
      ),
    cancel: refresh.cancel,
  };
  return {
    client,
    key,
    scheduler,
    suspend: () => {
      allowUpdates = false;
    },
    cleanup: () => {
      scheduler.cancel();
      unsubscribe();
      client.clear();
    },
  };
}

it("preserves a slow read and fetches changes received during it once afterward", async () => {
  let finish!: (rows: string[]) => void;
  const load = vi.fn(
    () =>
      new Promise<string[]>((resolve) => {
        finish = resolve;
      }),
  );
  const state = observeSessions(load);
  state.scheduler.schedule();
  for (let i = 0; i < 20; i++) {
    state.scheduler.schedule();
    await vi.advanceTimersByTimeAsync(1_000);
  }
  expect(load).toHaveBeenCalledTimes(1);
  expect(state.client.getQueryData(state.key)).toEqual(["existing"]);
  finish(["first"]);
  await vi.advanceTimersByTimeAsync(5_000);
  expect(load).toHaveBeenCalledTimes(2);
  finish(["latest"]);
  await vi.advanceTimersByTimeAsync(30_000);
  expect(load).toHaveBeenCalledTimes(2);
  expect(state.client.getQueryData(state.key)).toEqual(["latest"]);
  state.cleanup();
});

it("catches up after an initial HTTP read that predates the session event", async () => {
  let finish!: (rows: string[]) => void;
  const load = vi.fn(
    () =>
      new Promise<string[]>((resolve) => {
        finish = resolve;
      }),
  );
  const state = observeSessions(load);
  const initialRead = state.client.refetchQueries({ queryKey: state.key });
  state.scheduler.schedule();
  expect(load).toHaveBeenCalledTimes(1);
  finish(["initial"]);
  await initialRead;
  await vi.advanceTimersByTimeAsync(5_000);
  expect(load).toHaveBeenCalledTimes(2);
  finish(["latest"]);
  await vi.advanceTimersByTimeAsync(5_000);
  state.cleanup();
});

it("catches up stats already fetching when sessions are idle", async () => {
  const sessions = vi.fn(async () => ["latest"]);
  const state = observeSessions(sessions);
  let finishStats!: (count: number) => void;
  const stats = vi.fn(
    () =>
      new Promise<number>((resolve) => {
        finishStats = resolve;
      }),
  );
  const key = adminStatsKey(captureProfileRequestContext()!);
  state.client.setQueryData(key, 0);
  const observer = new QueryObserver(state.client, { queryKey: key, queryFn: stats });
  const unsubscribe = observer.subscribe(() => {});
  expect(stats).toHaveBeenCalledTimes(1);
  expect(sessions).not.toHaveBeenCalled();
  state.scheduler.schedule();
  expect(stats).toHaveBeenCalledTimes(1);
  finishStats(1);
  await vi.advanceTimersByTimeAsync(5_000);
  expect(stats).toHaveBeenCalledTimes(2);
  finishStats(2);
  await vi.advanceTimersByTimeAsync(30_000);
  expect(stats).toHaveBeenCalledTimes(2);
  expect(state.client.getQueryData(key)).toBe(2);
  unsubscribe();
  state.cleanup();
});

it.each(["profile", "proof", "unmount", "inactive dashboard"])(
  "does not run a queued refresh after %s changes",
  async (change) => {
    const load = vi.fn(async () => ["latest"]);
    const state = observeSessions(load);
    state.scheduler.schedule();
    await vi.advanceTimersByTimeAsync(0);
    state.scheduler.schedule();
    if (change === "profile") setProfileId("other");
    if (change === "proof") setProfileToken("replacement-proof");
    if (change === "unmount") state.scheduler.cancel();
    if (change === "inactive dashboard") state.suspend();
    await vi.advanceTimersByTimeAsync(10_000);
    expect(load).toHaveBeenCalledTimes(1);
    expect(state.client.getQueryState(state.key)?.isInvalidated).toBe(
      change === "inactive dashboard",
    );
    state.cleanup();
  },
);
