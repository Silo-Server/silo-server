import { api } from "@/api/client";

/**
 * Browser-side Web Push subscription helpers. The server's VAPID public key
 * comes from the notifications capability endpoint; subscriptions are
 * profile-scoped server-side.
 */

export type WebPushSupport = "supported" | "unsupported" | "denied";

export function webPushSupport(): WebPushSupport {
  if (
    !("serviceWorker" in navigator) ||
    !("PushManager" in window) ||
    !("Notification" in window)
  ) {
    return "unsupported";
  }
  if (Notification.permission === "denied") {
    return "denied";
  }
  return "supported";
}

function urlBase64ToUint8Array(base64: string): Uint8Array {
  const padding = "=".repeat((4 - (base64.length % 4)) % 4);
  const normalized = (base64 + padding).replace(/-/g, "+").replace(/_/g, "/");
  const raw = window.atob(normalized);
  const output = new Uint8Array(raw.length);
  for (let i = 0; i < raw.length; i += 1) {
    output[i] = raw.charCodeAt(i);
  }
  return output;
}

async function pushRegistration(): Promise<ServiceWorkerRegistration> {
  const registration = await navigator.serviceWorker.register("/sw.js");
  await navigator.serviceWorker.ready;
  return registration;
}

/** Returns the browser's current push subscription, if any. */
export async function currentWebPushSubscription(): Promise<PushSubscription | null> {
  if (webPushSupport() === "unsupported") {
    return null;
  }
  try {
    const registration = await navigator.serviceWorker.getRegistration("/sw.js");
    return (await registration?.pushManager.getSubscription()) ?? null;
  } catch {
    return null;
  }
}

function describeDevice(): string {
  const ua = navigator.userAgent;
  const browser = /firefox/i.test(ua)
    ? "Firefox"
    : /edg\//i.test(ua)
      ? "Edge"
      : /chrome|chromium/i.test(ua)
        ? "Chrome"
        : /safari/i.test(ua)
          ? "Safari"
          : "Browser";
  const platform = /windows/i.test(ua)
    ? "Windows"
    : /mac os/i.test(ua)
      ? "macOS"
      : /android/i.test(ua)
        ? "Android"
        : /iphone|ipad/i.test(ua)
          ? "iOS"
          : /linux/i.test(ua)
            ? "Linux"
            : "";
  return platform ? `${browser} on ${platform}` : browser;
}

/**
 * Requests permission, subscribes this browser, and registers the
 * subscription with the server for the active profile. Throws with a
 * user-presentable message on failure.
 */
export async function enableWebPush(vapidPublicKey: string): Promise<void> {
  if (webPushSupport() === "unsupported") {
    throw new Error("This browser does not support push notifications");
  }
  const permission = await Notification.requestPermission();
  if (permission !== "granted") {
    throw new Error("Notification permission was not granted");
  }
  const registration = await pushRegistration();
  const subscription = await registration.pushManager.subscribe({
    userVisibleOnly: true,
    applicationServerKey: urlBase64ToUint8Array(vapidPublicKey) as BufferSource,
  });
  const json = subscription.toJSON();
  if (!json.endpoint || !json.keys?.p256dh || !json.keys?.auth) {
    throw new Error("The browser returned an incomplete push subscription");
  }
  await api("/notifications/web-push/subscriptions", {
    method: "POST",
    body: JSON.stringify({
      endpoint: json.endpoint,
      keys: { p256dh: json.keys.p256dh, auth: json.keys.auth },
      device_name: describeDevice(),
    }),
  });
}

/** Unsubscribes this browser and removes the server-side registration. */
export async function disableWebPush(): Promise<void> {
  const subscription = await currentWebPushSubscription();
  if (!subscription) {
    return;
  }
  const endpoint = subscription.endpoint;
  await subscription.unsubscribe();
  await api("/notifications/web-push/unsubscribe", {
    method: "POST",
    body: JSON.stringify({ endpoint }),
  });
}
