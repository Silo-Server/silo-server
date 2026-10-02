import { describe, expect, it, vi } from "vitest";
import { prefetchRouteChunks, type RouteChunkScheduler } from "./routeChunkPrefetch";

/** Collects scheduled tasks so a test drives the idle queue explicitly. */
function manualScheduler() {
  const tasks: Array<() => void> = [];
  let cancelledCount = 0;
  const schedule: RouteChunkScheduler = (task) => {
    tasks.push(task);
    return () => {
      cancelledCount += 1;
    };
  };
  return {
    schedule,
    cancelledCount: () => cancelledCount,
    pending: () => tasks.length,
    async runNext() {
      const task = tasks.shift();
      task?.();
      // Let the import promise and its `finally` continuation settle so the
      // next warm-up is queued before the assertion runs.
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    },
  };
}

describe("prefetchRouteChunks", () => {
  it("warms one chunk per idle window, in order", async () => {
    const order: string[] = [];
    const scheduler = manualScheduler();

    prefetchRouteChunks(
      [
        () => {
          order.push("first");
          return Promise.resolve();
        },
        () => {
          order.push("second");
          return Promise.resolve();
        },
      ],
      scheduler.schedule,
    );

    expect(order).toEqual([]);

    await scheduler.runNext();
    expect(order).toEqual(["first"]);

    await scheduler.runNext();
    expect(order).toEqual(["first", "second"]);
    expect(scheduler.pending()).toBe(0);
  });

  it("continues past a chunk that fails to load", async () => {
    const second = vi.fn(() => Promise.resolve());
    const scheduler = manualScheduler();

    prefetchRouteChunks([() => Promise.reject(new Error("offline")), second], scheduler.schedule);

    await scheduler.runNext();
    await scheduler.runNext();

    expect(second).toHaveBeenCalledOnce();
  });

  it("stops warming after cancellation", async () => {
    const second = vi.fn(() => Promise.resolve());
    const scheduler = manualScheduler();

    const cancel = prefetchRouteChunks([() => Promise.resolve(), second], scheduler.schedule);

    await scheduler.runNext();
    expect(scheduler.pending()).toBe(1);

    cancel();
    await scheduler.runNext();

    expect(second).not.toHaveBeenCalled();
    expect(scheduler.cancelledCount()).toBe(1);
  });
});
