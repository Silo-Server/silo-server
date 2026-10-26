import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  bootstrapAccessToken,
  getAccessToken,
  getAuthContextVersion,
  lastRefreshFailureWasTransient,
  onRoleChanged,
  onSessionRejected,
  refreshAuthentication,
  SessionRefreshUnavailableError,
  setAccessToken,
  setRefreshToken,
} from "./client";
import { API_READ_TIMEOUT_MS } from "./requestDeadline";
import { v2 } from "./v2/request";
import { storage } from "../utils/storage";

function refreshedTokens(accessToken: string, refreshToken: string): Response {
  return new Response(
    JSON.stringify({ access_token: accessToken, refresh_token: refreshToken, expires_in: 3600 }),
    { status: 200, headers: { "Content-Type": "application/json" } },
  );
}

describe("bootstrapAccessToken", () => {
  beforeEach(() => {
    const localStorageState = new Map<string, string>();

    Object.defineProperty(globalThis, "localStorage", {
      value: {
        get length() {
          return localStorageState.size;
        },
        getItem: (key: string) => localStorageState.get(key) ?? null,
        key: (index: number) => Array.from(localStorageState.keys())[index] ?? null,
        setItem: (key: string, value: string) => {
          localStorageState.set(key, value);
        },
        removeItem: (key: string) => {
          localStorageState.delete(key);
        },
        clear: () => {
          localStorageState.clear();
        },
      } satisfies Storage,
      configurable: true,
    });

    localStorage.clear();
    setAccessToken(null);
    setRefreshToken(null);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    setAccessToken(null);
    setRefreshToken(null);
  });

  it("refreshes the access token before protected requests on startup", async () => {
    setRefreshToken("fake");
    const fetchMock = vi.fn<typeof fetch>(async (input) => {
      expect(String(input)).toBe("/api/v2/auth/refresh");
      return refreshedTokens("dummy", "example");
    });
    vi.stubGlobal("fetch", fetchMock);
    const signedOutContext = getAuthContextVersion();

    await expect(bootstrapAccessToken()).resolves.toBe(true);

    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(getAccessToken()).toBe("dummy");
    expect(localStorage.getItem("refresh_token")).toBe("example");
    // Establishing a session is an authority change, unlike a token rotation.
    expect(getAuthContextVersion()).not.toBe(signedOutContext);
  });

  it("does not refresh when an access token is already present", async () => {
    setAccessToken("sample");
    setRefreshToken("fake");
    const fetchMock = vi.fn<typeof fetch>();
    vi.stubGlobal("fetch", fetchMock);

    await expect(bootstrapAccessToken()).resolves.toBe(true);

    expect(fetchMock).not.toHaveBeenCalled();
    expect(getAccessToken()).toBe("sample");
  });

  it("shares one refresh with a request that meets a 401 during the restore", async () => {
    setRefreshToken("stored");
    let finishRefresh!: (response: Response) => void;
    const fetchMock = vi.fn<typeof fetch>(
      () =>
        new Promise<Response>((resolve) => {
          finishRefresh = resolve;
        }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const restore = bootstrapAccessToken();
    const joined = refreshAuthentication();
    finishRefresh(refreshedTokens("fresh", "rotated"));

    await expect(Promise.all([restore, joined])).resolves.toEqual([true, true]);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(getAccessToken()).toBe("fresh");
  });

  it("holds a request sent during the restore until the restored token exists", async () => {
    setRefreshToken("stored");
    let finishRefresh!: (response: Response) => void;
    const fetchMock = vi.fn<typeof fetch>(async (input, init) => {
      if (String(input) === "/api/v2/auth/refresh") {
        return new Promise<Response>((resolve) => {
          finishRefresh = resolve;
        });
      }
      const headers = init?.headers as Record<string, string>;
      expect(headers.Authorization).toBe("Bearer fresh");
      return Response.json({ items: [], avatar_upload_enabled: false });
    });
    vi.stubGlobal("fetch", fetchMock);

    const restore = bootstrapAccessToken();
    const profiles = v2("GET /api/v2/profiles");
    await Promise.resolve();
    expect(fetchMock).toHaveBeenCalledTimes(1);

    finishRefresh(refreshedTokens("fresh", "rotated"));

    await expect(restore).resolves.toBe(true);
    await expect(profiles).resolves.toEqual({ items: [], avatar_upload_enabled: false });
    expect(fetchMock.mock.calls.map(([input]) => String(input))).toEqual([
      "/api/v2/auth/refresh",
      "/api/v2/profiles",
    ]);
  });
});

describe("session rejection", () => {
  const rejected = vi.fn();

  beforeEach(() => {
    const localStorageState = new Map<string, string>();
    Object.defineProperty(globalThis, "localStorage", {
      value: {
        get length() {
          return localStorageState.size;
        },
        getItem: (key: string) => localStorageState.get(key) ?? null,
        key: (index: number) => Array.from(localStorageState.keys())[index] ?? null,
        setItem: (key: string, value: string) => {
          localStorageState.set(key, value);
        },
        removeItem: (key: string) => {
          localStorageState.delete(key);
        },
        clear: () => {
          localStorageState.clear();
        },
      } satisfies Storage,
      configurable: true,
    });
    setAccessToken(null);
    setRefreshToken(null);
    rejected.mockReset();
    onSessionRejected(rejected);
  });

  afterEach(() => {
    onSessionRejected(null);
    vi.unstubAllGlobals();
    setAccessToken(null);
    setRefreshToken(null);
  });

  function refreshProblem(status: number, id: string): Response {
    return Response.json(
      { type: `https://siloserver.org/docs/api/v2/problems/${id}`, title: id, status },
      { status, headers: { "Content-Type": "application/problem+json" } },
    );
  }

  // A signed-in request meets a 401, and the refresh answers with `status` and
  // the problem `id`.
  function signedInRequestWithRefresh(status: number, id: string) {
    setAccessToken("active");
    setRefreshToken("stored");
    const fetchMock = vi.fn<typeof fetch>(async (input) =>
      String(input) === "/api/v2/auth/refresh"
        ? refreshProblem(status, id)
        : refreshProblem(401, "authentication_required"),
    );
    vi.stubGlobal("fetch", fetchMock);
    return v2("GET /api/v2/profiles").catch(() => undefined);
  }

  it("reports a signed-in session whose refresh the server answers session_expired", async () => {
    await signedInRequestWithRefresh(401, "session_expired");
    expect(rejected).toHaveBeenCalledTimes(1);
  });

  it.each([
    // The server also answers its own failures (a database error) this way.
    [401, "invalid_token"],
    [400, "validation_failed"],
    [429, "rate_limited"],
    [500, "internal_error"],
    [503, "dependency_unavailable"],
  ])("keeps the session when the refresh fails with %i %s", async (status, id) => {
    await signedInRequestWithRefresh(status, id);
    expect(rejected).not.toHaveBeenCalled();
  });

  it("keeps the session when a refused refresh has no problem body", async () => {
    setAccessToken("active");
    setRefreshToken("stored");
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async () => new Response("Unauthorized", { status: 401 })),
    );
    await v2("GET /api/v2/profiles").catch(() => undefined);
    expect(rejected).not.toHaveBeenCalled();
  });

  it("keeps the session when the refresh cannot reach the server", async () => {
    setAccessToken("active");
    setRefreshToken("stored");
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async (input) => {
        if (String(input) === "/api/v2/auth/refresh") throw new TypeError("offline");
        return Response.json({ error: "invalid_token" }, { status: 401 });
      }),
    );
    await v2("GET /api/v2/profiles").catch(() => undefined);
    expect(rejected).not.toHaveBeenCalled();
  });

  it("gives up on a refresh the server never answers and keeps the session", async () => {
    vi.useFakeTimers();
    try {
      setAccessToken("active");
      setRefreshToken("stored");
      const fetchMock = vi.fn<typeof fetch>((input, init) => {
        if (String(input) !== "/api/v2/auth/refresh") {
          return Promise.resolve(refreshProblem(401, "authentication_required"));
        }
        return new Promise<Response>((_resolve, reject) => {
          init?.signal?.addEventListener("abort", () => reject(init.signal?.reason), {
            once: true,
          });
        });
      });
      vi.stubGlobal("fetch", fetchMock);

      const request = v2("GET /api/v2/profiles").then(
        () => "resolved",
        () => "rejected",
      );
      await vi.advanceTimersByTimeAsync(API_READ_TIMEOUT_MS);
      await expect(request).resolves.toBe("rejected");
      expect(rejected).not.toHaveBeenCalled();
      expect(getAccessToken()).toBe("active");
      expect(vi.getTimerCount()).toBe(0);

      // The abandoned exchange no longer holds the refresh single-flight.
      const refreshCalls = () =>
        fetchMock.mock.calls.filter(([input]) => String(input) === "/api/v2/auth/refresh").length;
      expect(refreshCalls()).toBe(1);
      void refreshAuthentication();
      expect(refreshCalls()).toBe(2);
    } finally {
      vi.useRealTimers();
    }
  });

  it("leaves a refused boot restore to the restore path", async () => {
    setRefreshToken("revoked");
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async () => refreshProblem(401, "session_expired")),
    );
    await expect(bootstrapAccessToken()).resolves.toBe(false);
    expect(rejected).not.toHaveBeenCalled();
    expect(lastRefreshFailureWasTransient()).toBe(false);
  });

  it("does not take a 401 whose body the deadline cut off as a refusal", async () => {
    vi.useFakeTimers();
    try {
      setRefreshToken("stored");
      // The status line arrives, then the problem body stalls until the
      // refresh deadline aborts the exchange.
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
          return new Response(body, {
            status: 401,
            headers: { "Content-Type": "application/problem+json" },
          });
        }),
      );

      const restore = bootstrapAccessToken();
      await vi.advanceTimersByTimeAsync(API_READ_TIMEOUT_MS);

      await expect(restore).resolves.toBe(false);
      // The boot restore keeps the stored session for a transient failure.
      expect(lastRefreshFailureWasTransient()).toBe(true);
      expect(storage.get(storage.KEYS.REFRESH_TOKEN)).toBe("stored");

      // A request whose 401 that refresh was answering fails as unavailable,
      // not with the 401.
      const fetchMock = vi.mocked(fetch);
      const stalledRefresh = fetchMock.getMockImplementation()!;
      fetchMock.mockImplementation(async (input, init) =>
        String(input) === "/api/v2/auth/refresh"
          ? stalledRefresh(input, init)
          : refreshProblem(401, "authentication_required"),
      );
      const request = v2("GET /api/v2/profiles", { timeoutMs: false }).catch(
        (error: unknown) => error,
      );
      await vi.advanceTimersByTimeAsync(API_READ_TIMEOUT_MS);
      expect(await request).toBeInstanceOf(SessionRefreshUnavailableError);
      expect(rejected).not.toHaveBeenCalled();
    } finally {
      vi.useRealTimers();
    }
  });

  it("ignores a refusal for a session that was replaced during the refresh", async () => {
    setAccessToken("old");
    setRefreshToken("old-refresh");
    let finishRefresh!: (response: Response) => void;
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(
        () =>
          new Promise<Response>((resolve) => {
            finishRefresh = resolve;
          }),
      ),
    );
    const refresh = refreshAuthentication();
    setAccessToken("new-account");
    finishRefresh(refreshProblem(401, "session_expired"));
    await expect(refresh).resolves.toBe(false);
    expect(rejected).not.toHaveBeenCalled();
  });

  describe("after a role change", () => {
    const roleChanged = vi.fn();

    beforeEach(() => {
      roleChanged.mockReset();
      onRoleChanged(roleChanged);
    });

    afterEach(() => onRoleChanged(null));

    // Requests carrying `stale` are refused with token_refresh_required; the
    // refresh answers with `refresh`.
    function staleTokenServer(refresh: () => Response) {
      const fetchMock = vi.fn<typeof fetch>(async (input, init) => {
        if (String(input) === "/api/v2/auth/refresh") return refresh();
        const authorization = new Headers(init?.headers).get("Authorization");
        return authorization === "Bearer stale"
          ? refreshProblem(401, "token_refresh_required")
          : Response.json({ items: [] });
      });
      vi.stubGlobal("fetch", fetchMock);
      return fetchMock;
    }

    it("refreshes, retries once and keeps the session", async () => {
      setAccessToken("stale");
      setRefreshToken("stored");
      const fetchMock = staleTokenServer(() => refreshedTokens("fresh", "rotated"));
      await expect(v2("GET /api/v2/profiles")).resolves.toBeDefined();
      expect(fetchMock).toHaveBeenCalledTimes(3);
      expect(getAccessToken()).toBe("fresh");
      expect(localStorage.getItem(storage.KEYS.REFRESH_TOKEN)).toBe("rotated");
      expect(rejected).not.toHaveBeenCalled();
      expect(roleChanged).toHaveBeenCalledTimes(1);
    });

    it("reports the change once for concurrent requests", async () => {
      setAccessToken("stale");
      setRefreshToken("stored");
      const fetchMock = staleTokenServer(() => refreshedTokens("fresh", "rotated"));
      await Promise.all([v2("GET /api/v2/profiles"), v2("GET /api/v2/profiles")]);
      const refreshes = fetchMock.mock.calls.filter(
        ([input]) => String(input) === "/api/v2/auth/refresh",
      );
      expect(refreshes).toHaveLength(1);
      expect(roleChanged).toHaveBeenCalledTimes(1);
      expect(rejected).not.toHaveBeenCalled();
    });

    it("signs out only when the refresh itself is refused with session_expired", async () => {
      setAccessToken("stale");
      setRefreshToken("stored");
      staleTokenServer(() => refreshProblem(401, "session_expired"));
      await v2("GET /api/v2/profiles").catch(() => undefined);
      expect(rejected).toHaveBeenCalledTimes(1);
      expect(roleChanged).not.toHaveBeenCalled();
    });
  });

  it("ignores a refusal after another tab stored a new session", async () => {
    setAccessToken("stale-tab");
    setRefreshToken("old-refresh");
    let finishRefresh!: (response: Response) => void;
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(
        () =>
          new Promise<Response>((resolve) => {
            finishRefresh = resolve;
          }),
      ),
    );
    const refresh = refreshAuthentication();
    // Another tab signs in and writes its refresh token to shared storage;
    // this tab's in-memory access token is untouched.
    localStorage.setItem(storage.KEYS.REFRESH_TOKEN, "other-tab-refresh");
    finishRefresh(refreshProblem(401, "session_expired"));
    await expect(refresh).resolves.toBe(false);
    expect(rejected).not.toHaveBeenCalled();
    expect(localStorage.getItem(storage.KEYS.REFRESH_TOKEN)).toBe("other-tab-refresh");
  });
});
