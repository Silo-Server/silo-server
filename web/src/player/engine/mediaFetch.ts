import type { WorkerToPageMessage } from "./protocol";

/** How long a read's newest data may wait before the page hears of it. */
const MEDIA_DATA_REPORT_MS = 250;

/**
 * One read worker's run: the bytes every range, retry and head request of it
 * has received, and when the first and latest arrived. The page joins the runs
 * of one source into one request. `spans` holds one `[first, last]` per
 * response, from its first body chunk to its latest: the time the run was
 * receiving. Time waiting for headers, or between ranges, is in none.
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
 * run, each call is its own request.
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
    let span: [number, number] | undefined;
    const counted = response.body.pipeThrough(
      new TransformStream<Uint8Array, Uint8Array>({
        transform(chunk, controller) {
          counting.last = performance.timeOrigin + performance.now();
          if (counting.bytes === 0) counting.first = counting.last;
          if (span) span[1] = counting.last;
          else counting.spans.push((span = [counting.last, counting.last]));
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
