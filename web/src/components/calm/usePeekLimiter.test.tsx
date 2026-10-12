import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  createLimiter,
  usePeekLimiter,
  type PeekRequest,
  type PreviewItem,
} from "./usePeekLimiter";

function deferred<T = void>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

const flush = () => new Promise((resolve) => setTimeout(resolve, 0));

describe("createLimiter", () => {
  it("never runs more than its limit at once and starts waiting tasks in order", async () => {
    const run = createLimiter(4);
    const gates = Array.from({ length: 7 }, () => deferred());
    const started: number[] = [];
    let active = 0;
    let peak = 0;
    const results = gates.map((gate, index) =>
      run(async () => {
        started.push(index);
        active += 1;
        peak = Math.max(peak, active);
        await gate.promise;
        active -= 1;
        return index;
      }),
    );
    await flush();
    expect(started).toEqual([0, 1, 2, 3]);

    gates[2]!.resolve();
    await flush();
    expect(started).toEqual([0, 1, 2, 3, 4]);

    gates.forEach((gate) => gate.resolve());
    expect(await Promise.all(results)).toEqual([0, 1, 2, 3, 4, 5, 6]);
    expect(peak).toBe(4);
  });

  it("frees the slot when a task fails", async () => {
    const run = createLimiter(1);
    await expect(run(async () => Promise.reject(new Error("boom")))).rejects.toThrow("boom");
    await expect(run(async () => "next")).resolves.toBe("next");
  });

  it("drops a waiting task whose signal aborts, without giving it a slot", async () => {
    const run = createLimiter(1);
    const gate = deferred();
    const first = run(() => gate.promise);
    const controller = new AbortController();
    let ranAborted = false;
    const aborted = run(async () => {
      ranAborted = true;
    }, controller.signal);
    const third = run(async () => "third");
    controller.abort();
    await expect(aborted).rejects.toBeDefined();

    gate.resolve();
    await first;
    await expect(third).resolves.toBe("third");
    expect(ranAborted).toBe(false);
  });

  it("refuses a task whose signal has already aborted", async () => {
    const run = createLimiter(1);
    const controller = new AbortController();
    controller.abort();
    let ran = false;
    await expect(
      run(async () => {
        ran = true;
      }, controller.signal),
    ).rejects.toBeDefined();
    expect(ran).toBe(false);
  });
});

describe("usePeekLimiter", () => {
  let observers: Array<{ callback: IntersectionObserverCallback; targets: Set<Element> }>;

  function revealAll() {
    act(() => {
      for (const observer of observers)
        for (const target of observer.targets)
          observer.callback(
            [{ isIntersecting: true, target } as IntersectionObserverEntry],
            {} as IntersectionObserver,
          );
    });
  }

  function Peek({ request, name }: { request: PeekRequest; name: string }) {
    const { observe, items } = usePeekLimiter(request);
    return (
      <div ref={observe} data-testid={name}>
        {items.map((item) => item.title).join(",")}
      </div>
    );
  }

  beforeEach(() => {
    observers = [];
    vi.stubGlobal(
      "IntersectionObserver",
      class {
        readonly root = null;
        readonly rootMargin = "0px";
        readonly thresholds = [0];
        private readonly record: (typeof observers)[number];
        constructor(callback: IntersectionObserverCallback) {
          this.record = { callback, targets: new Set() };
          observers.push(this.record);
        }
        observe = (target: Element) => this.record.targets.add(target);
        unobserve = (target: Element) => this.record.targets.delete(target);
        disconnect = () => this.record.targets.clear();
        takeRecords = () => [];
      },
    );
  });
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("waits for the screen, then never has more than 4 peeks in flight across the page", async () => {
    const gates = Array.from({ length: 6 }, () => deferred<PreviewItem[]>());
    let active = 0;
    let peak = 0;
    const fetch = vi.fn(async (index: number) => {
      active += 1;
      peak = Math.max(peak, active);
      const items = await gates[index]!.promise;
      active -= 1;
      return items;
    });
    const client = new QueryClient();
    render(
      <QueryClientProvider client={client}>
        {gates.map((_, index) => (
          <Peek
            key={index}
            name={`peek-${index}`}
            request={{ queryKey: ["calm-peek", index], fetch: () => fetch(index) }}
          />
        ))}
      </QueryClientProvider>,
    );
    await act(flush);
    expect(fetch).not.toHaveBeenCalled();

    revealAll();
    await act(flush);
    expect(fetch).toHaveBeenCalledTimes(4);

    gates[0]!.resolve([{ id: "x", title: "Arrival" }]);
    await waitFor(() => expect(screen.getByTestId("peek-0")).toHaveTextContent("Arrival"));
    await waitFor(() => expect(fetch).toHaveBeenCalledTimes(5));

    gates.forEach((gate) => gate.resolve([]));
    await waitFor(() => expect(fetch).toHaveBeenCalledTimes(6));
    expect(peak).toBe(4);
  });
});
