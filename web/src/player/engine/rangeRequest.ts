/** Helpers for one range request of `ParallelRangeSource`. */

/**
 * A signal for one request: it aborts when `signal` (the read worker's) aborts, and when the
 * request goes `ms` without `touch()`, which a received chunk calls. The timer starts here, before
 * the fetch. `dispose()` ends the timer and the link to `signal`, and aborts the request's signal,
 * which frees a response that nobody read to its end. A deadline abort ends only this request.
 */
export function inactivitySignal(
  signal: AbortSignal,
  ms: number,
): { signal: AbortSignal; touch: () => void; dispose: () => void } {
  const controller = new AbortController();
  const abort = () => controller.abort();
  let timer: ReturnType<typeof setTimeout>;
  const touch = () => {
    clearTimeout(timer);
    timer = setTimeout(abort, ms);
  };
  const dispose = () => {
    clearTimeout(timer);
    signal.removeEventListener("abort", abort);
    abort();
  };
  if (signal.aborted) abort();
  else signal.addEventListener("abort", abort, { once: true });
  touch();
  return { signal: controller.signal, touch, dispose };
}

/**
 * The protocol the browser reports for the latest completed response from `url` (`http/1.1`,
 * `h2`, `h3`), or an empty string when it reports none: a cross-origin response says only when
 * its server sent `Timing-Allow-Origin`.
 */
export function responseProtocol(url: string | URL | Request): string {
  const href = url instanceof Request ? url.url : String(url);
  const name = typeof location === "undefined" ? href : new URL(href, location.href).href;
  const entries = performance.getEntriesByName?.(name, "resource") as
    | PerformanceResourceTiming[]
    | undefined;
  return entries?.[entries.length - 1]?.nextHopProtocol ?? "";
}
