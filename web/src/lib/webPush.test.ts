import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import { disableWebPush, enableWebPush } from "./webPush";

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
    const result = enableWebPush("AQID");
    if (status === 201) await result;
    else await expect(result).rejects.toThrow();
    expect(subscribe).toHaveBeenCalledTimes(1);
    expect(fetch).toHaveBeenCalledTimes(1);
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
        return { toJSON: () => ({}) };
      });
    const fetch = vi.fn();
    vi.stubGlobal("fetch", fetch);
    await expect(enableWebPush("AQID")).rejects.toThrow();
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
  await expect(enableWebPush("AQID")).rejects.toThrow();
  expect(fetch).toHaveBeenCalledTimes(1);
});
