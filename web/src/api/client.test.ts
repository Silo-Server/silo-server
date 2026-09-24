import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  bootstrapAccessToken,
  getAccessToken,
  getAuthContextVersion,
  refreshAuthentication,
  setAccessToken,
  setRefreshToken,
} from "./client";
import { v2 } from "./v2/request";

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

describe("client helper inventory", () => {
  it("does not expose the legacy person-items helper anymore", async () => {
    const clientModule = await import("./client");

    expect(clientModule).not.toHaveProperty("getPersonItems");
  });
});
