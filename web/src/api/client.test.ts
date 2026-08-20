import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  api,
  apiBlob,
  apiWithProfileRequestContext,
  bootstrapAccessToken,
  captureProfileRequestContext,
  getAccessToken,
  getProfileToken,
  getPersonCatalogItems,
  onProfileUnverified,
  setAccessToken,
  setProfileId,
  setProfileToken,
  setRefreshToken,
} from "./client";

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

  it("refreshes the access token before protected requests on startup", async () => {
    setRefreshToken("fake");
    const fetchMock = vi.fn<typeof fetch>(async (input) => {
      expect(String(input)).toBe("/api/v1/auth/refresh");
      return new Response(
        JSON.stringify({
          access_token: "dummy",
          refresh_token: "example",
          expires_in: 3600,
        }),
        {
          status: 200,
          headers: { "Content-Type": "application/json" },
        },
      );
    });

    await expect(bootstrapAccessToken(fetchMock)).resolves.toBe(true);

    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(getAccessToken()).toBe("dummy");
    expect(localStorage.getItem("refresh_token")).toBe("example");
  });

  it("does not refresh when an access token is already present", async () => {
    setAccessToken("sample");
    setRefreshToken("fake");
    const fetchMock = vi.fn<typeof fetch>();

    await expect(bootstrapAccessToken(fetchMock)).resolves.toBe(true);

    expect(fetchMock).not.toHaveBeenCalled();
    expect(getAccessToken()).toBe("sample");
  });
});

describe("getPersonCatalogItems", () => {
  it("requests person filmography through the catalog API", async () => {
    Object.defineProperty(globalThis, "sessionStorage", {
      value: {
        getItem: () => null,
        setItem: () => {},
        removeItem: () => {},
        clear: () => {},
      },
      configurable: true,
    });

    const fetchMock = vi.fn<typeof fetch>(async (input) => {
      expect(String(input)).toBe("/api/v1/catalog?source=person&person_id=123&limit=24&offset=0");
      return new Response(
        JSON.stringify({
          total: 0,
          has_more: false,
          items: [],
        }),
        {
          status: 200,
          headers: { "Content-Type": "application/json" },
        },
      );
    });

    vi.stubGlobal("fetch", fetchMock);

    await expect(getPersonCatalogItems("123", undefined, 24, 0)).resolves.toEqual({
      total: 0,
      has_more: false,
      items: [],
    });

    expect(fetchMock).toHaveBeenCalledTimes(1);
  });
});

describe("client helper inventory", () => {
  it("does not expose the legacy person-items helper anymore", async () => {
    const clientModule = await import("./client");

    expect(clientModule).not.toHaveProperty("getPersonItems");
  });
});

describe("apiBlob", () => {
  beforeEach(() => {
    Object.defineProperty(globalThis, "sessionStorage", {
      value: {
        getItem: () => null,
        setItem: () => {},
        removeItem: () => {},
        clear: () => {},
      },
      configurable: true,
    });
  });

  it("rejects responses whose Content-Length exceeds the in-memory cap", async () => {
    const fetchMock = vi.fn<typeof fetch>(async () => {
      const res = new Response("x", { status: 200 });
      // 3 GiB; Response normally derives Content-Length from the body, so
      // override the header lookup instead of materializing a huge body.
      vi.spyOn(res.headers, "get").mockImplementation((name) =>
        name.toLowerCase() === "content-length" ? String(3 * 1024 * 1024 * 1024) : null,
      );
      return res;
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(apiBlob("/ebooks/abc/files/1/read")).rejects.toMatchObject({
      name: "ApiClientError",
      code: "response_too_large",
      message: expect.stringContaining("too large to open in the browser"),
    });
  });

  it("returns the blob when Content-Length is within the cap or missing", async () => {
    const fetchMock = vi.fn<typeof fetch>(async () => {
      const res = new Response("epub-bytes", { status: 200 });
      vi.spyOn(res.headers, "get").mockReturnValue(null);
      return res;
    });
    vi.stubGlobal("fetch", fetchMock);

    const blob = await apiBlob("/ebooks/abc/files/1/read");
    await expect(blob.text()).resolves.toBe("epub-bytes");
  });
});

describe("api", () => {
  it("keeps the originating profile isolated when retrying after token refresh", async () => {
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
    Object.defineProperty(globalThis, "sessionStorage", {
      value: {
        getItem: () => null,
        setItem: () => {},
        removeItem: () => {},
        clear: () => {},
      },
      configurable: true,
    });
    setAccessToken("old");
    setRefreshToken("old");
    setProfileId("profile-old");
    setProfileToken("old");
    const profileUnverified = vi.fn();
    onProfileUnverified(profileUnverified);

    let protectedRequestCount = 0;
    const fetchMock = vi.fn<typeof fetch>(async (input) => {
      const path = String(input);
      if (path === "/api/v1/auth/refresh") {
        // Model a household profile switch while refresh is in flight.
        setProfileId("profile-new");
        setProfileToken("new");
        return new Response(
          JSON.stringify({
            access_token: "new",
            refresh_token: "new",
            expires_in: 3600,
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        );
      }
      protectedRequestCount += 1;
      return protectedRequestCount === 1
        ? new Response(null, { status: 401 })
        : new Response(
            JSON.stringify({
              error: "profile_unverified",
              message: "Profile verification required.",
            }),
            { status: 403, headers: { "Content-Type": "application/json" } },
          );
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(
      api("/settings/values/ui.library_page_state?scope=profile_device", {
        method: "PUT",
        body: JSON.stringify({ value: { version: 1, libraries: {} } }),
      }),
    ).rejects.toMatchObject({ status: 403, code: "profile_unverified" });

    const requestCalls = fetchMock.mock.calls.filter(
      ([input]) => String(input) !== "/api/v1/auth/refresh",
    );
    expect(requestCalls).toHaveLength(2);
    const firstHeaders = requestCalls[0]?.[1]?.headers as Record<string, string>;
    const retryHeaders = requestCalls[1]?.[1]?.headers as Record<string, string>;
    expect(firstHeaders).toMatchObject({
      Authorization: "Bearer old",
      "X-Profile-Id": "profile-old",
      "X-Profile-Token": "old",
    });
    expect(retryHeaders).toMatchObject({
      Authorization: "Bearer new",
      "X-Profile-Id": "profile-old",
      "X-Profile-Token": "old",
    });
    expect(localStorage.getItem("profile_id")).toBe("profile-new");
    expect(localStorage.getItem("profile_token")).toBe("new");
    expect(profileUnverified).not.toHaveBeenCalled();
    onProfileUnverified(null);
  });

  it("preserves an explicitly captured profile identity", async () => {
    Object.defineProperty(globalThis, "sessionStorage", {
      value: {
        getItem: () => null,
        setItem: () => {},
        removeItem: () => {},
        clear: () => {},
      },
      configurable: true,
    });
    setProfileId("profile-new");
    const fetchMock = vi.fn<typeof fetch>(
      async () =>
        new Response(JSON.stringify({ ok: true }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
    );
    vi.stubGlobal("fetch", fetchMock);

    await api("/test", { headers: { "X-Profile-Id": "profile-old" } });

    const headers = fetchMock.mock.calls[0]![1]!.headers as Record<string, string>;
    expect(headers["X-Profile-Id"]).toBe("profile-old");
  });

  it("preserves the PIN token captured with an explicit profile identity", async () => {
    Object.defineProperty(globalThis, "sessionStorage", {
      value: {
        getItem: () => null,
        setItem: () => {},
        removeItem: () => {},
        clear: () => {},
      },
      configurable: true,
    });
    setProfileId("profile-new");
    setProfileToken("dummy");
    const fetchMock = vi.fn<typeof fetch>(
      async () =>
        new Response(JSON.stringify({ ok: true }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
    );
    vi.stubGlobal("fetch", fetchMock);

    try {
      await api("/test", {
        headers: {
          "X-Profile-Id": "profile-old",
          "X-Profile-Token": "fake",
        },
      });

      const headers = fetchMock.mock.calls[0]![1]!.headers as Record<string, string>;
      expect(headers["X-Profile-Id"]).toBe("profile-old");
      expect(headers["X-Profile-Token"]).toBe("fake");
    } finally {
      setProfileToken(null);
    }
  });

  it("safely refreshes a captured request without rebasing its profile authority", async () => {
    Object.defineProperty(globalThis, "sessionStorage", {
      value: {
        getItem: () => null,
        setItem: () => {},
        removeItem: () => {},
        clear: () => {},
      },
      configurable: true,
    });
    setAccessToken("fake");
    setProfileId("profile-old");
    setProfileToken("fake");
    setRefreshToken("dummy");
    const snapshot = captureProfileRequestContext();
    expect(snapshot).not.toBeNull();
    const fetchMock = vi.fn<typeof fetch>(async (input, init) => {
      const url = String(input);
      if (url === "/api/v1/auth/refresh") {
        expect(JSON.parse(String(init?.body))).toEqual({ refresh_token: "dummy" });
        return Response.json({
          access_token: "example",
          refresh_token: "sample",
          expires_in: 3600,
        });
      }
      const headers = init?.headers as Record<string, string>;
      expect(headers["X-Profile-Id"]).toBe("profile-old");
      expect(headers["X-Profile-Token"]).toBe("fake");
      if (headers.Authorization === "Bearer fake") {
        return Response.json({ error: "unauthorized", message: "expired" }, { status: 401 });
      }
      expect(headers.Authorization).toBe("Bearer example");
      return Response.json({ ok: true });
    });
    vi.stubGlobal("fetch", fetchMock);

    try {
      await expect(
        apiWithProfileRequestContext("/test", snapshot!, { method: "PUT" }),
      ).resolves.toEqual({ ok: true });

      expect(fetchMock).toHaveBeenCalledTimes(3);
      expect(getAccessToken()).toBe("example");
      expect(localStorage.getItem("refresh_token")).toBe("sample");
    } finally {
      setRefreshToken(null);
      setProfileToken(null);
      setAccessToken(null);
    }
  });

  it("clears the active PIN when its captured profile authority is rejected", async () => {
    setAccessToken("fake");
    setProfileId("profile-old");
    setProfileToken("pin-old");
    const snapshot = captureProfileRequestContext();
    expect(snapshot).not.toBeNull();
    const listener = vi.fn();
    onProfileUnverified(listener);
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async () =>
        Response.json({ error: "profile_unverified", message: "PIN required" }, { status: 403 }),
      ),
    );

    try {
      await expect(apiWithProfileRequestContext("/test", snapshot!)).rejects.toMatchObject({
        status: 403,
        code: "profile_unverified",
      });
      expect(getProfileToken()).toBeNull();
      expect(listener).toHaveBeenCalledOnce();
    } finally {
      onProfileUnverified(null);
      setProfileToken(null);
      setProfileId(null);
      setAccessToken(null);
    }
  });

  it.each([
    {
      authorityChange: "active profile",
      changeAuthority: () => {
        setProfileId("profile-new");
        setProfileToken("pin-new");
      },
      expectedToken: "pin-new",
    },
    {
      authorityChange: "active PIN",
      changeAuthority: () => setProfileToken("pin-new"),
      expectedToken: "pin-new",
    },
  ])(
    "does not clear the $authorityChange for a delayed rejection of captured authority",
    async ({ changeAuthority, expectedToken }) => {
      setAccessToken("fake");
      setProfileId("profile-old");
      setProfileToken("pin-old");
      const snapshot = captureProfileRequestContext();
      expect(snapshot).not.toBeNull();
      const listener = vi.fn();
      onProfileUnverified(listener);
      let resolveResponse!: (response: Response) => void;
      const response = new Promise<Response>((resolve) => {
        resolveResponse = resolve;
      });
      const fetchMock = vi.fn<typeof fetch>(() => response);
      vi.stubGlobal("fetch", fetchMock);

      try {
        const request = apiWithProfileRequestContext("/test", snapshot!);
        await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledOnce());
        changeAuthority();
        resolveResponse(
          Response.json({ error: "profile_unverified", message: "PIN required" }, { status: 403 }),
        );

        await expect(request).rejects.toMatchObject({
          status: 403,
          code: "profile_unverified",
        });
        expect(getProfileToken()).toBe(expectedToken);
        expect(listener).not.toHaveBeenCalled();
      } finally {
        onProfileUnverified(null);
        setProfileToken(null);
        setProfileId(null);
        setAccessToken(null);
      }
    },
  );

  it.each(["account", "origin"] as const)(
    "cancels captured retry when the %s changes during refresh",
    async (changedContext) => {
      Object.defineProperty(globalThis, "sessionStorage", {
        value: {
          getItem: () => null,
          setItem: () => {},
          removeItem: () => {},
          clear: () => {},
        },
        configurable: true,
      });
      const originalLocation = globalThis.location;
      if (changedContext === "origin") {
        Object.defineProperty(globalThis, "location", {
          value: { origin: "https://server-old.example" },
          configurable: true,
        });
      }
      setAccessToken("fake");
      setProfileId("profile-old");
      setProfileToken("fake");
      setRefreshToken("dummy");
      const snapshot = captureProfileRequestContext();
      expect(snapshot).not.toBeNull();

      let resolveRefresh!: (response: Response) => void;
      const refreshResponse = new Promise<Response>((resolve) => {
        resolveRefresh = resolve;
      });
      const fetchMock = vi.fn<typeof fetch>(async (input) => {
        if (String(input) === "/api/v1/auth/refresh") return refreshResponse;
        return Response.json({ error: "unauthorized", message: "expired" }, { status: 401 });
      });
      vi.stubGlobal("fetch", fetchMock);

      try {
        const request = apiWithProfileRequestContext("/test", snapshot!, { method: "PUT" });
        await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
        if (changedContext === "account") {
          setAccessToken("sample");
          setRefreshToken("placeholder");
        } else {
          Object.defineProperty(globalThis, "location", {
            value: { origin: "https://server-new.example" },
            configurable: true,
          });
        }
        resolveRefresh(
          Response.json({
            access_token: "example",
            refresh_token: "redacted",
            expires_in: 3600,
          }),
        );

        await expect(request).rejects.toMatchObject({ name: "StaleApiRequestContextError" });
        expect(fetchMock).toHaveBeenCalledTimes(2);
        expect(getAccessToken()).toBe(changedContext === "account" ? "sample" : "fake");
        expect(localStorage.getItem("refresh_token")).toBe(
          changedContext === "account" ? "placeholder" : "dummy",
        );
      } finally {
        if (changedContext === "origin") {
          Object.defineProperty(globalThis, "location", {
            value: originalLocation,
            configurable: true,
          });
        }
        setRefreshToken(null);
        setProfileToken(null);
        setAccessToken(null);
      }
    },
  );

  it("refuses a captured request after the account context changes", async () => {
    Object.defineProperty(globalThis, "sessionStorage", {
      value: {
        getItem: () => null,
        setItem: () => {},
        removeItem: () => {},
        clear: () => {},
      },
      configurable: true,
    });
    setAccessToken("fake");
    setProfileId("profile-old");
    setProfileToken("fake");
    const snapshot = captureProfileRequestContext();
    expect(snapshot).not.toBeNull();
    setAccessToken("dummy");
    setProfileId("profile-new");
    setProfileToken("dummy");
    const fetchMock = vi.fn<typeof fetch>();
    vi.stubGlobal("fetch", fetchMock);

    try {
      await expect(apiWithProfileRequestContext("/test", snapshot!)).rejects.toMatchObject({
        name: "StaleApiRequestContextError",
      });
      expect(fetchMock).not.toHaveBeenCalled();
    } finally {
      setProfileToken(null);
    }
  });

  it("keeps the browser client family canonical", async () => {
    Object.defineProperty(globalThis, "sessionStorage", {
      value: {
        getItem: () => null,
        setItem: () => {},
        removeItem: () => {},
        clear: () => {},
      },
      configurable: true,
    });
    const fetchMock = vi.fn<typeof fetch>(
      async () =>
        new Response(JSON.stringify({ ok: true }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
    );
    vi.stubGlobal("fetch", fetchMock);

    await api("/test", { headers: { "x-silo-client-family": "tv" } });

    const headers = fetchMock.mock.calls[0]![1]!.headers as Record<string, string>;
    expect(headers["X-Silo-Client-Family"]).toBe("web");
    expect(
      Object.keys(headers).filter((key) => key.toLowerCase() === "x-silo-client-family"),
    ).toEqual(["X-Silo-Client-Family"]);
  });

  it("forwards AbortSignal from options to fetch", async () => {
    Object.defineProperty(globalThis, "sessionStorage", {
      value: {
        getItem: () => null,
        setItem: () => {},
        removeItem: () => {},
        clear: () => {},
      },
      configurable: true,
    });

    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ ok: true }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const controller = new AbortController();
    await api("/test", { signal: controller.signal });

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const call = fetchMock.mock.calls[0]!;
    const init = call[1] as RequestInit;
    expect(init.signal).toBe(controller.signal);
  });

  it("treats 202 responses with an empty body as success", async () => {
    Object.defineProperty(globalThis, "sessionStorage", {
      value: {
        getItem: () => null,
        setItem: () => {},
        removeItem: () => {},
        clear: () => {},
      },
      configurable: true,
    });

    const fetchMock = vi.fn<typeof fetch>(async () => new Response(null, { status: 202 }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(
      api("/webhook-sync/connections/abc/webhook/rotate", { method: "POST" }),
    ).resolves.toBeUndefined();
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });
});
