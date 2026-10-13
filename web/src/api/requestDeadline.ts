/**
 * How long the web app waits for the server to answer an ordinary read or a
 * token refresh before giving up on it. A server that accepts the connection
 * and then stops answering (a frozen process, a black-holed network, a stuck
 * reverse proxy) would otherwise hold the page on a blank loading state until
 * the browser or proxy gives up, which takes minutes.
 */
export const API_READ_TIMEOUT_MS = 30_000;

/** A request's abort signal bounded by a deadline. */
export interface RequestDeadline {
  /** Aborts on the deadline or when the caller's signal aborts, whichever is first. */
  readonly signal: AbortSignal;
  /** Whether the deadline, not the caller, aborted the request. */
  readonly expired: boolean;
  /** Stops the timer and detaches from the caller's signal; call once the request settles. */
  dispose(): void;
}

/**
 * Starts a deadline of `timeoutMs` for one request. The returned signal aborts
 * with `timeoutReason()` when the deadline passes and with the caller's own
 * reason when `callerSignal` aborts first, so a cancelled query still looks
 * cancelled. The two are linked by hand rather than with `AbortSignal.any` so
 * `dispose` releases the timer and the listener as soon as the request settles,
 * and so browsers without `AbortSignal.any` (Safari before 17.4) behave the
 * same.
 */
export function startRequestDeadline(
  timeoutMs: number,
  timeoutReason: () => unknown,
  callerSignal?: AbortSignal | null,
): RequestDeadline {
  const controller = new AbortController();
  let expired = false;
  let timer: ReturnType<typeof setTimeout> | undefined;
  const forwardAbort = () => controller.abort(callerSignal?.reason);
  if (callerSignal?.aborted) {
    controller.abort(callerSignal.reason);
  } else {
    callerSignal?.addEventListener("abort", forwardAbort, { once: true });
    timer = setTimeout(() => {
      if (controller.signal.aborted) return;
      expired = true;
      controller.abort(timeoutReason());
    }, timeoutMs);
  }
  return {
    signal: controller.signal,
    get expired() {
      return expired;
    },
    dispose() {
      clearTimeout(timer);
      callerSignal?.removeEventListener("abort", forwardAbort);
    },
  };
}
