import { describe, expect, it } from "vitest";

import { runBatch } from "./batch";

function deferred() {
  let resolve!: () => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<void>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

const flush = () => new Promise((resolve) => setTimeout(resolve, 0));

describe("runBatch", () => {
  it("keeps at most four requests in flight and starts the next as one finishes", async () => {
    const pending = new Map<number, ReturnType<typeof deferred>>();
    let inFlight = 0;
    let most = 0;
    const done = runBatch(
      Array.from({ length: 9 }, (_, index) => index),
      async (item) => {
        inFlight++;
        most = Math.max(most, inFlight);
        const answer = deferred();
        pending.set(item, answer);
        try {
          await answer.promise;
        } finally {
          inFlight--;
        }
      },
      () => "failed",
    );
    await flush();
    expect([...pending.keys()]).toEqual([0, 1, 2, 3]);

    pending.get(2)!.resolve();
    await flush();
    expect([...pending.keys()]).toEqual([0, 1, 2, 3, 4]);

    for (let item = 0; item < 9; item++) {
      pending.get(item)?.resolve();
      await flush();
    }
    for (const answer of pending.values()) answer.resolve();
    await expect(done).resolves.toEqual({ done: 9, failures: [] });
    expect(most).toBe(4);
  });

  it("finishes every item when some fail, and names the failures in the order given", async () => {
    const result = await runBatch(
      ["Alpha", "Beta", "Gamma", "Delta"],
      async (item) => {
        // Beta fails after Delta, so the order comes from the input, not the answers.
        await new Promise((resolve) => setTimeout(resolve, item === "Beta" ? 5 : 0));
        if (item === "Beta" || item === "Delta") throw new Error(`${item} broke`);
      },
      (item, error) => `${item}: ${(error as Error).message}`,
    );
    expect(result).toEqual({
      done: 2,
      failures: ["Beta: Beta broke", "Delta: Delta broke"],
    });
  });

  it("does nothing for an empty batch", async () => {
    await expect(
      runBatch(
        [],
        async () => undefined,
        () => "",
      ),
    ).resolves.toEqual({ done: 0, failures: [] });
  });
});
