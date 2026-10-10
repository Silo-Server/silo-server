/**
 * Select mode's actions on several server collections: one request per
 * collection, a few at a time, finishing every one even when some fail.
 */

/** The most collections one select-mode action works on, as on Home rows. */
export const MAX_SELECTED_COLLECTIONS = 100;

/** Requests a select-mode action keeps in flight at once. */
const BATCH_PARALLEL = 4;

export interface BatchResult<F> {
  /** How many finished. */
  done: number;
  /** One entry per item that failed, in the order the items were given. */
  failures: F[];
}

/**
 * Runs `run` on every item with at most `BATCH_PARALLEL` running; each one
 * that finishes starts the next. A failure is recorded with `failure` and
 * the rest still run.
 */
export async function runBatch<T, F>(
  items: readonly T[],
  run: (item: T) => Promise<unknown>,
  failure: (item: T, error: unknown) => F,
): Promise<BatchResult<F>> {
  const failed: Array<F | undefined> = new Array(items.length);
  let done = 0;
  let next = 0;
  async function worker() {
    while (next < items.length) {
      const index = next++;
      const item = items[index]!;
      try {
        await run(item);
        done++;
      } catch (error) {
        failed[index] = failure(item, error);
      }
    }
  }
  await Promise.all(Array.from({ length: Math.min(BATCH_PARALLEL, items.length) }, worker));
  return { done, failures: failed.filter((entry): entry is F => entry !== undefined) };
}
