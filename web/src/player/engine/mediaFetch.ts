import type { WorkerToPageMessage } from "./protocol";

/** How long a read's newest data may wait before the page hears of it. */
const MEDIA_DATA_REPORT_MS = 250;

/**
 * One metered request: the bytes its responses have received, and when the
 * first and latest arrived. The striped reader counts every range, retry and
 * head request of a source into one run; a stock read is a run of its own.
 * `spans` is the time the run was receiving: the union of each response's time
 * from its first body chunk to its latest, as sorted `[first, last]` pairs that
 * do not overlap. Time waiting for headers, or between ranges, is in none. The
 * list has one entry per stretch of receiving, not one per response, so a long
 * play keeps it short.
 */
export interface MediaRun {
  request: number;
  bytes: number;
  first: number;
  last: number;
  spans: [number, number][];
  report: ReturnType<typeof setTimeout> | null;
}

/**
 * Fetches for the source and tells the page what each response body
 * receives, so it can tell a slow link, whose data keeps arriving, from a
 * silent one, and measure the plan's throughput.
 *
 * `fetch` counts into the run it is given, so a reader that sends many short
 * ranges reports them as one long request, which the page's throughput meter
 * can use, for the time at least one of its responses was receiving. Without a
 * run, each call is its own request, as a stock read is: the meter's gate on
 * short requests then applies to each one.
 */
export function createMeteredFetch(post: (message: WorkerToPageMessage) => void) {
  let nextMediaRequest = 0;

  function newRun(): MediaRun {
    return { request: nextMediaRequest++, bytes: 0, first: 0, last: 0, spans: [], report: null };
  }

  async function meteredFetch(
    input: RequestInfo | URL,
    init?: RequestInit,
    run?: MediaRun,
  ): Promise<Response> {
    const response = await fetch(input, init);
    if (!response.body) return response;
    const counting = run ?? newRun();
    // When this response's previous chunk arrived; undefined before its first.
    let previous: number | undefined;
    const counted = response.body.pipeThrough(
      new TransformStream<Uint8Array, Uint8Array>({
        transform(chunk, controller) {
          counting.last = performance.timeOrigin + performance.now();
          if (counting.bytes === 0) counting.first = counting.last;
          // This response was receiving from its previous chunk until now. Now is never
          // earlier than a time already in the list, so the stretch joins every entry it
          // reaches back into, from the tail.
          let from = previous ?? counting.last;
          const spans = counting.spans;
          while (spans.length > 0 && (spans[spans.length - 1] as [number, number])[1] >= from) {
            from = Math.min(from, (spans.pop() as [number, number])[0]);
          }
          spans.push([from, counting.last]);
          previous = counting.last;
          counting.bytes += chunk.byteLength;
          counting.report ??= setTimeout(() => {
            counting.report = null;
            post({
              type: "media-data",
              request: counting.request,
              bytes: counting.bytes,
              first: counting.first,
              last: counting.last,
              spans: counting.spans.map(([first, last]) => [first, last]),
            });
          }, MEDIA_DATA_REPORT_MS);
          controller.enqueue(chunk);
        },
      }),
    );
    // Mediabunny also reads the response's url, type and redirect flag, which a
    // new Response around the counted body would lose.
    Object.defineProperty(response, "body", { value: counted });
    return response;
  }

  return { fetch: meteredFetch, newRun };
}
