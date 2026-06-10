import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  api,
  apiBlob,
  bootstrapAccessToken,
  getAccessToken,
  getPersonCatalogItems,
  setAccessToken,
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
    setRefreshToken("startup-refresh");
    const fetchMock = vi.fn<typeof fetch>(async (input) => {
      expect(String(input)).toBe("/api/v1/auth/refresh");
      return new Response(
        JSON.stringify({
          access_token: "startup-access",
          refresh_token: "rotated-refresh",
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
    expect(getAccessToken()).toBe("startup-access");
    expect(localStorage.getItem("refresh_token")).toBe("rotated-refresh");
  });

  it("does not refresh when an access token is already present", async () => {
    setAccessToken("already-present");
    setRefreshToken("startup-refresh");
    const fetchMock = vi.fn<typeof fetch>();

    await expect(bootstrapAccessToken(fetchMock)).resolves.toBe(true);

    expect(fetchMock).not.toHaveBeenCalled();
    expect(getAccessToken()).toBe("already-present");
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
