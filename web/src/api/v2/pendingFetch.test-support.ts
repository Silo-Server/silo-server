import { vi } from "vitest";

/**
 * Fetch stubs for wrapper tests that change authority while a request is in
 * flight. Not a test file itself; imported by tests in this directory.
 */

/** A response the test delivers by hand once it has changed authority. */
export function pendingResponse() {
  let finish!: (response: Response) => void;
  const promise = new Promise<Response>((resolve) => {
    finish = resolve;
  });
  return { promise, finish };
}

/**
 * Stubs fetch so each request waits until the test calls `finish` with its
 * response. `finish` throws when no request has been sent yet.
 */
export function stubPendingFetch() {
  let finish!: (response: Response) => void;
  const fetch = vi.fn<typeof globalThis.fetch>(
    () =>
      new Promise<Response>((resolve) => {
        finish = resolve;
      }),
  );
  vi.stubGlobal("fetch", fetch);
  return { fetch, finish: (response: Response) => finish(response) };
}
