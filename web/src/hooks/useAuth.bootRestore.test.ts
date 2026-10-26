import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import authenticationRequired from "../../../contracts/api/v2/fixtures/authentication_required.json";
import {
  bootstrapAccessToken,
  getAccessToken,
  setAccessToken,
  setRefreshToken,
} from "@/api/client";
import { API_READ_TIMEOUT_MS } from "@/api/requestDeadline";
import { v2 } from "@/api/v2/request";
import { storage } from "@/utils/storage";
import { initializeAuthSession } from "./useAuth";

// The boot restore against the real session client: the first refresh
// succeeds, the account read meets a 401, and the refresh that should answer
// it gets no verdict from the server.

function refreshed(accessToken: string): Response {
  return Response.json(
    { access_token: accessToken, refresh_token: "stored", expires_in: 3600 },
    { status: 200 },
  );
}

function problem(status: number, body: unknown): Response {
  return Response.json(body, {
    status,
    headers: { "Content-Type": "application/problem+json" },
  });
}

const dependencyUnavailable = {
  type: "https://siloserver.org/docs/api/v2/problems/dependency_unavailable",
  title: "Unavailable",
  status: 503,
  detail: "Try later.",
  instance: "/api/v2/auth/refresh",
};

function restore() {
  const calls = {
    clearTokens: vi.fn(() => {
      setAccessToken(null);
      setRefreshToken(null);
    }),
    clearActiveAuthState: vi.fn(),
    markRestoreUnavailable: vi.fn(),
    applyCurrentUser: vi.fn(),
  };
  const done = initializeAuthSession({
    refreshToken: storage.get(storage.KEYS.REFRESH_TOKEN),
    hasStoredImpersonationAdminSession: false,
    bootstrapAccessToken,
    fetchCurrentUser: () => v2("GET /api/v2/account/me"),
    applyCurrentUser: calls.applyCurrentUser,
    restoreProfile: vi.fn(),
    recoverPreservedAdminSession: vi.fn<() => Promise<boolean>>().mockResolvedValue(false),
    clearTokens: calls.clearTokens,
    clearActiveAuthState: calls.clearActiveAuthState,
    markRestoreUnavailable: calls.markRestoreUnavailable,
  });
  return { calls, done };
}

describe("boot restore when a later refresh gets no verdict", () => {
  beforeEach(() => {
    localStorage.clear();
    setAccessToken(null);
    setRefreshToken("stored");
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
    setAccessToken(null);
    setRefreshToken(null);
  });

  it("keeps the session when the account read's refresh answers 503", async () => {
    let refreshes = 0;
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async (input) => {
        if (String(input) === "/api/v2/auth/refresh") {
          refreshes += 1;
          return refreshes === 1 ? refreshed("first") : problem(503, dependencyUnavailable);
        }
        return problem(401, authenticationRequired);
      }),
    );

    const { calls, done } = restore();
    await done;

    expect(refreshes).toBe(2);
    expect(calls.markRestoreUnavailable).toHaveBeenCalledTimes(1);
    expect(calls.clearTokens).not.toHaveBeenCalled();
    expect(storage.get(storage.KEYS.REFRESH_TOKEN)).toBe("stored");
    expect(getAccessToken()).toBe("first");
  });

  it("keeps the session when the account read's refresh times out", async () => {
    vi.useFakeTimers();
    let refreshes = 0;
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>((input, init) => {
        if (String(input) === "/api/v2/auth/refresh") {
          refreshes += 1;
          if (refreshes === 1) return Promise.resolve(refreshed("first"));
          return new Promise<Response>((_resolve, reject) => {
            init?.signal?.addEventListener("abort", () => reject(init.signal?.reason), {
              once: true,
            });
          });
        }
        return Promise.resolve(problem(401, authenticationRequired));
      }),
    );

    const { calls, done } = restore();
    await vi.advanceTimersByTimeAsync(2 * API_READ_TIMEOUT_MS);
    await done;

    expect(calls.markRestoreUnavailable).toHaveBeenCalledTimes(1);
    expect(calls.clearTokens).not.toHaveBeenCalled();
    expect(storage.get(storage.KEYS.REFRESH_TOKEN)).toBe("stored");
  });

  it("signs out when the account read's refresh is refused", async () => {
    let refreshes = 0;
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async (input) => {
        if (String(input) === "/api/v2/auth/refresh") {
          refreshes += 1;
          return refreshes === 1 ? refreshed("first") : problem(401, authenticationRequired);
        }
        return problem(401, authenticationRequired);
      }),
    );

    const { calls, done } = restore();
    await done;

    expect(calls.clearTokens).toHaveBeenCalledTimes(1);
    expect(calls.markRestoreUnavailable).not.toHaveBeenCalled();
    expect(storage.get(storage.KEYS.REFRESH_TOKEN)).toBeNull();
  });
});
