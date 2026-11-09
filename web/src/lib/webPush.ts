import type { ProfileRequestContextSnapshot } from "@/api/client";
import { listNotificationWebPushSubscriptions } from "@/api/v2/notificationDestinations";
import { captureNotificationAuthority, requireNotificationAuthority } from "@/api/v2/notifications";
import { v2 } from "@/api/v2/request";
import { storage } from "@/utils/storage";

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
 * Who turned on push for this browser's subscription, kept across sign-out. The
 * server stops sending once the registering login ends and retention then
 * removes the row, so this is what lets a later sign-in by the same account and
 * profile register again, and tells it apart from someone else signing in on
 * this browser. A null loginSessionId is a login that could not be read.
 */
interface WebPushOwner {
  endpoint: string;
  userId: number;
  profileId: string;
  loginSessionId: string | null;
}

function readWebPushOwner(): WebPushOwner | null {
  const raw = storage.get(storage.KEYS.WEB_PUSH_OWNER);
  if (!raw) return null;
  try {
    return JSON.parse(raw) as WebPushOwner;
  } catch {
    return null;
  }
}

function writeWebPushOwner(owner: WebPushOwner) {
  storage.set(storage.KEYS.WEB_PUSH_OWNER, JSON.stringify(owner));
}

async function currentLoginSessionId(): Promise<string | null> {
  const { current_session: session } = await v2("GET /api/v2/auth/sessions", {
    query: { limit: 1 },
  });
  return session?.id ?? null;
}

async function registerSubscription(
  json: PushSubscriptionJSON,
  context: ProfileRequestContextSnapshot,
): Promise<string> {
  if (!json.endpoint || !json.keys?.p256dh || !json.keys?.auth) {
    throw new Error("The browser returned an incomplete push subscription");
  }
  await v2("POST /api/v2/notifications/web-push/subscriptions", {
    profileContext: context,
    retryAuthentication: false,
    body: {
      endpoint: json.endpoint,
      keys: { p256dh: json.keys.p256dh, auth: json.keys.auth },
      device_name: describeDevice(),
    },
  });
  requireNotificationAuthority(context);
  return json.endpoint;
}

/**
 * Requests permission, subscribes this browser, and registers the
 * subscription with the server for the active profile. Throws with a
 * user-presentable message on failure.
 */
export async function enableWebPush(
  vapidPublicKey: string,
  userId: number,
  context = captureNotificationAuthority(),
): Promise<void> {
  requireNotificationAuthority(context);
  if (webPushSupport() === "unsupported") {
    throw new Error("This browser does not support push notifications");
  }
  const permission = await Notification.requestPermission();
  requireNotificationAuthority(context);
  if (permission !== "granted") {
    throw new Error("Notification permission was not granted");
  }
  const registration = await navigator.serviceWorker.register("/sw.js");
  requireNotificationAuthority(context);
  await navigator.serviceWorker.ready;
  requireNotificationAuthority(context);
  const subscription = await registration.pushManager.subscribe({
    userVisibleOnly: true,
    applicationServerKey: urlBase64ToUint8Array(vapidPublicKey) as BufferSource,
  });
  requireNotificationAuthority(context);
  const endpoint = await registerSubscription(subscription.toJSON(), context);
  // Push is already on; not knowing the login only costs one more registration
  // on a later load.
  const loginSessionId = await currentLoginSessionId().catch(() => null);
  requireNotificationAuthority(context);
  writeWebPushOwner({ endpoint, userId, profileId: context.profileId, loginSessionId });
}

/**
 * Registers this browser's existing subscription on the current login when it
 * belongs to this account and profile, so push follows a fresh sign-in and a
 * subscription made before the server recorded logins gets bound. It belongs
 * here if this browser remembers enabling it for them, or if the server lists it
 * under them. Anything else was someone else's choice and is left alone; turning
 * push on for a new person takes the Settings toggle. Resolves whether it
 * registered, and sends at most once per login.
 */
export async function syncWebPushSubscription(
  userId: number,
  context = captureNotificationAuthority(),
): Promise<boolean> {
  requireNotificationAuthority(context);
  if (webPushSupport() !== "supported" || Notification.permission !== "granted") return false;
  const subscription = await currentWebPushSubscription();
  requireNotificationAuthority(context);
  if (!subscription) return false;
  const loginSessionId = await currentLoginSessionId();
  requireNotificationAuthority(context);
  if (!loginSessionId) return false;
  const rows = await listNotificationWebPushSubscriptions(context);
  requireNotificationAuthority(context);

  const listed = rows.some((row) => row.endpoint === subscription.endpoint);
  const stored = readWebPushOwner();
  const remembered =
    stored?.endpoint === subscription.endpoint &&
    stored.userId === userId &&
    stored.profileId === context.profileId;
  if (remembered && stored.loginSessionId === loginSessionId) {
    // Registered on this login and now gone: removed from another device, or by
    // the push service. Forget it so a later sign-in does not bring it back. A
    // removal this browser does not see before its login ends looks the same as
    // retention, and the next sign-in does bring that one back.
    if (!listed) storage.remove(storage.KEYS.WEB_PUSH_OWNER);
    return false;
  }
  if (!remembered && !listed) return false;

  const endpoint = await registerSubscription(subscription.toJSON(), context);
  writeWebPushOwner({ endpoint, userId, profileId: context.profileId, loginSessionId });
  return true;
}

/** Unsubscribes this browser and removes the server-side registration. */
export async function disableWebPush(context = captureNotificationAuthority()): Promise<void> {
  requireNotificationAuthority(context);
  if (webPushSupport() === "unsupported") return;
  const registration = await navigator.serviceWorker.getRegistration("/sw.js");
  requireNotificationAuthority(context);
  const subscription = await registration?.pushManager.getSubscription();
  requireNotificationAuthority(context);
  if (!subscription) return;
  await v2("POST /api/v2/notifications/web-push/unsubscribe", {
    body: { endpoint: subscription.endpoint },
    profileContext: context,
    retryAuthentication: false,
  });
  storage.remove(storage.KEYS.WEB_PUSH_OWNER);
  requireNotificationAuthority(context);
  const removed = await subscription.unsubscribe();
  requireNotificationAuthority(context);
  if (!removed)
    throw new Error(
      "Server registration removed, but the browser subscription could not be removed.",
    );
}
