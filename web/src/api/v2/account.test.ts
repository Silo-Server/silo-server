import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { API_READ_TIMEOUT_MS } from "../requestDeadline";
import { restoreUserSession } from "./account";
import { V2TimeoutError } from "./request";

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

const unauthorized = () =>
  Response.json(
    {
      type: "https://siloserver.org/docs/api/v2/problems/authentication_required",
      title: "Unauthorized",
      status: 401,
      detail: "Sign in again.",
    },
    { status: 401, headers: { "Content-Type": "application/problem+json" } },
  );

beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
});

describe("restoreUserSession", () => {
  it("fails with V2TimeoutError when the account read gets no answer", async () => {
    const fetchImpl = hungFetch();

    const outcome = restoreUserSession({
      accessToken: "admin-access",
      refreshToken: "admin-refresh",
      fetchImpl,
    }).catch((error: unknown) => error);
    await vi.advanceTimersByTimeAsync(API_READ_TIMEOUT_MS);

    expect(await outcome).toBeInstanceOf(V2TimeoutError);
    expect(vi.getTimerCount()).toBe(0);
  });

  it("fails with V2TimeoutError when the refresh after a 401 gets no answer", async () => {
    const hung = hungFetch();
    const fetchImpl = vi.fn<typeof fetch>((input, init) =>
      String(input).endsWith("/auth/refresh") ? hung(input, init) : Promise.resolve(unauthorized()),
    );

    const outcome = restoreUserSession({
      accessToken: "expired-access",
      refreshToken: "admin-refresh",
      fetchImpl,
    }).catch((error: unknown) => error);
    await vi.advanceTimersByTimeAsync(API_READ_TIMEOUT_MS);

    expect(await outcome).toBeInstanceOf(V2TimeoutError);
    expect(fetchImpl).toHaveBeenCalledTimes(2);
  });

  it("refreshes after a 401 whose body breaks off", async () => {
    const brokenUnauthorized = unauthorized();
    Object.defineProperty(brokenUnauthorized, "text", {
      value: () => Promise.reject(new TypeError("network error")),
    });
    const fetchImpl = vi.fn<typeof fetch>(async (input, init) => {
      if (String(input).endsWith("/auth/refresh")) {
        return Response.json({ access_token: "new-access", refresh_token: "new-refresh" });
      }
      const expired = new Headers(init?.headers).get("Authorization") === "Bearer expired-access";
      return expired ? brokenUnauthorized : Response.json({ id: "1", username: "admin" });
    });

    const restored = await restoreUserSession({
      accessToken: "expired-access",
      refreshToken: "admin-refresh",
      fetchImpl,
    });

    expect(restored).toMatchObject({ accessToken: "new-access", refreshToken: "new-refresh" });
  });
});
