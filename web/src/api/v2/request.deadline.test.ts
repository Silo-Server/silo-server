import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import authenticationRequired from "../../../../contracts/api/v2/fixtures/authentication_required.json";

import { getOrCreateDeviceId, setAccessToken, setProfileId, setRefreshToken } from "../client";
import { API_READ_TIMEOUT_MS } from "../requestDeadline";
import { V2ProblemError, V2TimeoutError, v2 } from "./request";

const JSON_HEADERS = { "Content-Type": "application/json" };
const PROBLEM_HEADERS = { "Content-Type": "application/problem+json" };

function json(body: unknown, status = 200, headers: Record<string, string> = JSON_HEADERS) {
  return new Response(JSON.stringify(body), { status, headers });
}

// A server that accepted the connection and went quiet: the request settles
// only when its signal aborts, with the signal's reason, as a browser does.
function hungFetch() {
  return vi.fn<typeof fetch>(
    (_input, init) =>
      new Promise<Response>((_resolve, reject) => {
        if (init?.signal?.aborted) reject(init.signal.reason);
        init?.signal?.addEventListener("abort", () => reject(init.signal?.reason), {
          once: true,
        });
      }),
  );
}

beforeEach(() => {
  localStorage.clear();
  // Store the device id under real timers: jsdom queues a storage event on a
  // timer, which would otherwise count as a pending timer below.
  getOrCreateDeviceId();
  vi.useFakeTimers();
  setAccessToken("tok-user");
  setRefreshToken(null);
  setProfileId(null);
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  setAccessToken(null);
});

describe("v2 request deadline", () => {
  it("fails a read the server never answers with V2TimeoutError", async () => {
    const fetchMock = hungFetch();
    vi.stubGlobal("fetch", fetchMock);

    const outcome = v2("GET /api/v2/profiles").catch((error: unknown) => error);
    await vi.advanceTimersByTimeAsync(API_READ_TIMEOUT_MS - 1);
    expect(fetchMock.mock.calls[0]?.[1]?.signal?.aborted).toBe(false);

    await vi.advanceTimersByTimeAsync(1);
    const error = await outcome;
    expect(error).toBeInstanceOf(V2TimeoutError);
    expect(error).toMatchObject({ operationId: "listProfiles", timeoutMs: API_READ_TIMEOUT_MS });
    expect(vi.getTimerCount()).toBe(0);
  });

  it("fails a read whose body stalls after the headers", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async (_input, init) => {
        const body = new ReadableStream<Uint8Array>({
          start(controller) {
            init?.signal?.addEventListener("abort", () => controller.error(init.signal?.reason), {
              once: true,
            });
          },
        });
        return new Response(body, { status: 200, headers: JSON_HEADERS });
      }),
    );

    const outcome = v2("GET /api/v2/profiles").catch((error: unknown) => error);
    await vi.advanceTimersByTimeAsync(API_READ_TIMEOUT_MS);
    expect(await outcome).toBeInstanceOf(V2TimeoutError);
  });

  it("bounds the catalog browse, a read sent as POST", async () => {
    vi.stubGlobal("fetch", hungFetch());

    const outcome = v2("POST /api/v2/catalog/query", { body: {} }).catch((error: unknown) => error);
    await vi.advanceTimersByTimeAsync(API_READ_TIMEOUT_MS);
    expect(await outcome).toBeInstanceOf(V2TimeoutError);
  });

  it("honours a caller's own deadline", async () => {
    vi.stubGlobal("fetch", hungFetch());

    const outcome = v2("GET /api/v2/profiles", { timeoutMs: 1_000 }).catch(
      (error: unknown) => error,
    );
    await vi.advanceTimersByTimeAsync(1_000);
    expect(await outcome).toMatchObject({ name: "V2TimeoutError", timeoutMs: 1_000 });
  });

  it("rejects with the caller's reason when the caller aborts first", async () => {
    const fetchMock = hungFetch();
    vi.stubGlobal("fetch", fetchMock);
    const controller = new AbortController();
    const cancelled = new DOMException("query cancelled", "AbortError");

    const outcome = v2("GET /api/v2/profiles", { signal: controller.signal }).catch(
      (error: unknown) => error,
    );
    await vi.advanceTimersByTimeAsync(1_000);
    controller.abort(cancelled);

    expect(await outcome).toBe(cancelled);
    expect(fetchMock.mock.calls[0]?.[1]?.signal?.reason).toBe(cancelled);
    expect(vi.getTimerCount()).toBe(0);
  });

  it("does not wait on a read whose caller already aborted", async () => {
    vi.stubGlobal("fetch", hungFetch());
    const controller = new AbortController();
    controller.abort();

    const outcome = v2("GET /api/v2/profiles", { signal: controller.signal }).catch(
      (error: unknown) => error,
    );
    expect(await outcome).toBe(controller.signal.reason);
    expect(vi.getTimerCount()).toBe(0);
  });

  it("does not time out a read that opts out, or a write", async () => {
    const pending: Array<(response: Response) => void> = [];
    const fetchMock = vi.fn<typeof fetch>(
      () => new Promise<Response>((resolve) => pending.push(resolve)),
    );
    vi.stubGlobal("fetch", fetchMock);
    const controller = new AbortController();

    const read = v2("GET /api/v2/profiles", { timeoutMs: false, signal: controller.signal });
    const write = v2("POST /api/v2/notifications/read-all", { body: { through: "n-1" } });
    let settled = false;
    void Promise.allSettled([read, write]).then(() => {
      settled = true;
    });

    await vi.advanceTimersByTimeAsync(10 * API_READ_TIMEOUT_MS);
    expect(settled).toBe(false);
    expect(vi.getTimerCount()).toBe(0);
    // Without a deadline the caller's signal reaches fetch untouched.
    expect(fetchMock.mock.calls[0]?.[1]?.signal).toBe(controller.signal);
    expect(fetchMock.mock.calls[1]?.[1]?.signal).toBeUndefined();

    pending[0]?.(json({ items: [] }));
    pending[1]?.(new Response(null, { status: 204 }));
    await expect(read).resolves.toEqual({ items: [] });
    await expect(write).resolves.toBeUndefined();
  });

  it("clears the deadline once a response arrives", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async (input) =>
        String(input) === "/api/v2/profiles"
          ? json({ items: [] })
          : json(authenticationRequired, 401, PROBLEM_HEADERS),
      ),
    );

    await expect(v2("GET /api/v2/profiles")).resolves.toEqual({ items: [] });
    expect(vi.getTimerCount()).toBe(0);

    await expect(v2("GET /api/v2/account/me")).rejects.toBeInstanceOf(V2ProblemError);
    expect(vi.getTimerCount()).toBe(0);
  });
});
