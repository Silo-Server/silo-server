import {
  StaleApiRequestContextError,
  setAccessToken,
  setProfileId,
  setProfileToken,
} from "@/api/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { disableWebPush, enableWebPush, syncWebPushSubscription } from "./webPush";

beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("account");
  setProfileId("profile");
  setProfileToken(null);
  vi.stubGlobal("PushManager", class {});
  vi.stubGlobal("Notification", { permission: "granted" });
});
afterEach(() => vi.unstubAllGlobals());
function browser() {
  const unsubscribe = vi.fn().mockResolvedValue(true);
  const subscription = { endpoint: "https://push.example.test/opaque", unsubscribe };
  const getSubscription = vi.fn().mockResolvedValue(subscription);
  vi.stubGlobal("navigator", {
    userAgent: "Synthetic Browser",
    serviceWorker: {
      getRegistration: vi.fn().mockResolvedValue({ pushManager: { getSubscription } }),
    },
  });
  return { unsubscribe, getSubscription };
}
it("removes server registration before local unsubscribe", async () => {
  const { unsubscribe } = browser();
  const fetch = vi.fn<typeof globalThis.fetch>().mockImplementation(async () => {
    expect(unsubscribe).not.toHaveBeenCalled();
    return new Response(null, { status: 204 });
  });
  vi.stubGlobal("fetch", fetch);
  await disableWebPush();
  expect(fetch).toHaveBeenCalledTimes(1);
  expect(String(fetch.mock.calls[0]![0])).toContain("/api/v2/notifications/web-push/unsubscribe");
  expect(fetch.mock.calls[0]![1]?.body).toBe(
    JSON.stringify({ endpoint: "https://push.example.test/opaque" }),
  );
  expect(unsubscribe).toHaveBeenCalledTimes(1);
});
it("preserves local endpoint on failed or uncertain server removal without replay", async () => {
  for (const status of [401, 403, 500]) {
    const { unsubscribe } = browser();
    const fetch = vi.fn().mockResolvedValue(new Response(null, { status }));
    vi.stubGlobal("fetch", fetch);
    await expect(disableWebPush()).rejects.toThrow();
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(unsubscribe).not.toHaveBeenCalled();
  }
  const { unsubscribe } = browser();
  const fetch = vi.fn().mockRejectedValue(new TypeError("network interrupted"));
  vi.stubGlobal("fetch", fetch);
  await expect(disableWebPush()).rejects.toThrow();
  expect(fetch).toHaveBeenCalledTimes(1);
  expect(unsubscribe).not.toHaveBeenCalled();
});
it("fences authority replacement during browser discovery and HTTP", async () => {
  const first = browser();
  first.getSubscription.mockImplementation(async () => {
    setProfileToken("new-pin");
    return { endpoint: "opaque", unsubscribe: first.unsubscribe };
  });
  const fetch = vi.fn().mockImplementation(async () => {
    setProfileToken("next-pin");
    return new Response(null, { status: 204 });
  });
  vi.stubGlobal("fetch", fetch);
  await expect(disableWebPush()).rejects.toThrow();
  expect(fetch).not.toHaveBeenCalled();
  expect(first.unsubscribe).not.toHaveBeenCalled();
  const second = browser();
  await expect(disableWebPush()).rejects.toThrow();
  expect(fetch).toHaveBeenCalledTimes(1);
  expect(second.unsubscribe).not.toHaveBeenCalled();
});
it("surfaces local unsubscribe refusal after server success", async () => {
  const { unsubscribe } = browser();
  unsubscribe.mockResolvedValue(false);
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(null, { status: 204 })));
  await expect(disableWebPush()).rejects.toThrow("browser subscription could not be removed");
});

it("surfaces discovery failure without claiming local removal", async () => {
  const { getSubscription, unsubscribe } = browser();
  getSubscription.mockRejectedValue(new Error("browser storage unavailable"));
  const fetch = vi.fn();
  vi.stubGlobal("fetch", fetch);
  await expect(disableWebPush()).rejects.toThrow("browser storage unavailable");
  expect(fetch).not.toHaveBeenCalled();
  expect(unsubscribe).not.toHaveBeenCalled();
});

function registration() {
  const subscribe = vi.fn().mockResolvedValue({
    toJSON: () => ({
      endpoint: "https://push.example.test/opaque",
      keys: { p256dh: "synthetic-key", auth: "synthetic-auth" },
    }),
  });
  const register = vi.fn().mockResolvedValue({ pushManager: { subscribe } });
  const permission = vi.fn().mockResolvedValue("granted");
  vi.stubGlobal("Notification", { permission: "granted", requestPermission: permission });
  vi.stubGlobal("navigator", {
    userAgent: "Synthetic Browser",
    serviceWorker: { register, ready: Promise.resolve() },
  });
  return { subscribe, register, permission };
}
it("registers the browser once with write-only keys and no auth replay", async () => {
  for (const status of [201, 401, 403, 500]) {
    const { subscribe } = registration();
    const fetch = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ id: "row" }), {
        status,
        headers: { "Content-Type": "application/json" },
      }),
    );
    vi.stubGlobal("fetch", fetch);
    const result = enableWebPush("AQID", 7);
    if (status === 201) await result;
    else await expect(result).rejects.toThrow();
    expect(subscribe).toHaveBeenCalledTimes(1);
    const writes = fetch.mock.calls.filter(([, init]) => init?.method === "POST");
    expect(writes).toHaveLength(1);
    expect(String(fetch.mock.calls[0]![0])).toContain(
      "/api/v2/notifications/web-push/subscriptions",
    );
    expect(JSON.parse(fetch.mock.calls[0]![1].body)).toMatchObject({
      endpoint: "https://push.example.test/opaque",
      keys: { p256dh: "synthetic-key", auth: "synthetic-auth" },
    });
  }
});
it("fences registration after permission, service worker and subscription waits", async () => {
  for (const stage of ["permission", "register", "subscribe"] as const) {
    setProfileToken(null);
    const browser = registration();
    if (stage === "permission")
      browser.permission.mockImplementation(async () => {
        setProfileToken("new-pin");
        return "granted";
      });
    if (stage === "register")
      browser.register.mockImplementation(async () => {
        setProfileToken("new-pin");
        return { pushManager: { subscribe: browser.subscribe } };
      });
    if (stage === "subscribe")
      browser.subscribe.mockImplementation(async () => {
        setProfileToken("new-pin");
        return {
          toJSON: () => ({
            endpoint: "https://push.example.test/opaque",
            keys: { p256dh: "synthetic-key", auth: "synthetic-auth" },
          }),
        };
      });
    const fetch = vi.fn();
    vi.stubGlobal("fetch", fetch);
    await expect(enableWebPush("AQID", 7)).rejects.toThrow(StaleApiRequestContextError);
    expect(fetch).not.toHaveBeenCalled();
    if (stage === "permission") expect(browser.register).not.toHaveBeenCalled();
    if (stage !== "subscribe") expect(browser.subscribe).not.toHaveBeenCalled();
  }
});
it("rejects a stale registration receipt without a compensating mutation", async () => {
  registration();
  const fetch = vi.fn().mockImplementation(async () => {
    setProfileToken("replacement");
    return new Response(JSON.stringify({ id: "row" }), {
      status: 201,
      headers: { "Content-Type": "application/json" },
    });
  });
  vi.stubGlobal("fetch", fetch);
  await expect(enableWebPush("AQID", 7)).rejects.toThrow();
  expect(fetch).toHaveBeenCalledTimes(1);
});

const ENDPOINT = "https://push.example.test/opaque";
const OWNER_KEY = "silo-web-push-owner";

/** A browser already holding a push subscription, and a server answering the sync's reads. */
function syncBrowser({
  rows = [] as string[],
  loginSession = "login-new" as string | null,
  permission = "granted",
} = {}) {
  const subscription = {
    endpoint: ENDPOINT,
    toJSON: () => ({
      endpoint: ENDPOINT,
      keys: { p256dh: "synthetic-key", auth: "synthetic-auth" },
    }),
  };
  vi.stubGlobal("Notification", { permission });
  vi.stubGlobal("navigator", {
    userAgent: "Synthetic Browser",
    serviceWorker: {
      getRegistration: vi
        .fn()
        .mockResolvedValue({ pushManager: { getSubscription: async () => subscription } }),
    },
  });
  const json = (body: unknown, status = 200) =>
    new Response(JSON.stringify(body), {
      status,
      headers: { "Content-Type": "application/json" },
    });
  const fetch = vi.fn(async (url: RequestInfo | URL, init?: RequestInit) => {
    const path = String(url);
    if (path.includes("/auth/sessions"))
      return json({
        items: [],
        page: { has_more: false },
        current_session: loginSession ? { id: loginSession } : null,
      });
    if (init?.method === "POST") return json({ id: "row" }, 201);
    return json({
      items: rows.map((endpoint) => ({ id: "row", endpoint })),
      page: { has_more: false },
    });
  });
  vi.stubGlobal("fetch", fetch);
  const registrations = () =>
    fetch.mock.calls.filter(
      ([url, init]) =>
        init?.method === "POST" &&
        String(url).includes("/api/v2/notifications/web-push/subscriptions"),
    );
  return { fetch, registrations };
}
function owner(fields: Record<string, unknown> = {}) {
  localStorage.setItem(
    OWNER_KEY,
    JSON.stringify({
      endpoint: ENDPOINT,
      userId: 7,
      profileId: "profile",
      loginSessionId: "login-old",
      ...fields,
    }),
  );
}
const storedOwner = () => JSON.parse(localStorage.getItem(OWNER_KEY) ?? "null");

it("re-registers after a fresh sign-in when retention already removed the row", async () => {
  owner();
  const { registrations } = syncBrowser();
  await expect(syncWebPushSubscription(7)).resolves.toBe(true);
  expect(registrations()).toHaveLength(1);
  expect(JSON.parse(String(registrations()[0]![1]?.body))).toMatchObject({
    endpoint: ENDPOINT,
    keys: { p256dh: "synthetic-key", auth: "synthetic-auth" },
  });
  expect(storedOwner().loginSessionId).toBe("login-new");
});

it("binds a subscription from before the server recorded logins", async () => {
  const { registrations } = syncBrowser({ rows: [ENDPOINT] });
  await expect(syncWebPushSubscription(7)).resolves.toBe(true);
  expect(registrations()).toHaveLength(1);
  expect(storedOwner()).toMatchObject({
    endpoint: ENDPOINT,
    userId: 7,
    profileId: "profile",
    loginSessionId: "login-new",
  });
});

it("registers once per login", async () => {
  owner({ loginSessionId: "login-new" });
  const { registrations } = syncBrowser({ rows: [ENDPOINT] });
  await expect(syncWebPushSubscription(7)).resolves.toBe(false);
  expect(registrations()).toHaveLength(0);
});

it("never takes a subscription this account and profile did not make", async () => {
  for (const other of [{ userId: 8 }, { profileId: "sibling" }, { endpoint: "https://other" }]) {
    owner(other);
    const { registrations } = syncBrowser();
    await expect(syncWebPushSubscription(7)).resolves.toBe(false);
    expect(registrations()).toHaveLength(0);
  }
  localStorage.removeItem(OWNER_KEY);
  const { registrations } = syncBrowser();
  await expect(syncWebPushSubscription(7)).resolves.toBe(false);
  expect(registrations()).toHaveLength(0);
});

it("respects a removal made elsewhere during this login", async () => {
  owner({ loginSessionId: "login-new" });
  const first = syncBrowser();
  await expect(syncWebPushSubscription(7)).resolves.toBe(false);
  expect(first.registrations()).toHaveLength(0);
  expect(storedOwner()).toBeNull();

  const later = syncBrowser({ loginSession: "login-newer" });
  await expect(syncWebPushSubscription(7)).resolves.toBe(false);
  expect(later.registrations()).toHaveLength(0);
});

it("sends nothing without permission or a login session", async () => {
  owner();
  const denied = syncBrowser({ permission: "default" });
  await expect(syncWebPushSubscription(7)).resolves.toBe(false);
  expect(denied.fetch).not.toHaveBeenCalled();

  const keyed = syncBrowser({ loginSession: null });
  await expect(syncWebPushSubscription(7)).resolves.toBe(false);
  expect(keyed.registrations()).toHaveLength(0);
});

it("keeps the owner to retry when re-registering fails", async () => {
  owner();
  const { fetch } = syncBrowser();
  const answer = fetch.getMockImplementation()!;
  fetch.mockImplementation(async (url, init) =>
    init?.method === "POST" ? new Response(null, { status: 500 }) : answer(url, init),
  );
  await expect(syncWebPushSubscription(7)).rejects.toThrow();
  expect(storedOwner().loginSessionId).toBe("login-old");
});

it("stops at a profile switch before registering", async () => {
  owner();
  const { fetch, registrations } = syncBrowser();
  const answer = fetch.getMockImplementation()!;
  fetch.mockImplementation(async (url, init) => {
    if (String(url).includes("/auth/sessions")) setProfileToken("new-pin");
    return answer(url, init);
  });
  await expect(syncWebPushSubscription(7)).rejects.toThrow(StaleApiRequestContextError);
  expect(registrations()).toHaveLength(0);
});

it("remembers who enabled push on which login, and forgets on disable", async () => {
  const { fetch } = syncBrowser();
  const browserRegistration = registration();
  await enableWebPush("AQID", 7);
  expect(browserRegistration.subscribe).toHaveBeenCalledTimes(1);
  expect(fetch).toHaveBeenCalled();
  expect(storedOwner()).toEqual({
    endpoint: ENDPOINT,
    userId: 7,
    profileId: "profile",
    loginSessionId: "login-new",
  });

  browser();
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(null, { status: 204 })));
  await disableWebPush();
  expect(storedOwner()).toBeNull();
});

it("still enables when the login cannot be read", async () => {
  registration();
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: RequestInfo | URL) =>
      String(url).includes("/auth/sessions")
        ? new Response(null, { status: 500 })
        : new Response(JSON.stringify({ id: "row" }), {
            status: 201,
            headers: { "Content-Type": "application/json" },
          }),
    ),
  );
  await enableWebPush("AQID", 7);
  expect(storedOwner().loginSessionId).toBeNull();
});
