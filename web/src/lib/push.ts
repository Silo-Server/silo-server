/** Convert a base64url VAPID public key to the Uint8Array applicationServerKey wants. */
export function urlBase64ToUint8Array(base64: string): Uint8Array {
  const padding = "=".repeat((4 - (base64.length % 4)) % 4);
  const b64 = (base64 + padding).replace(/-/g, "+").replace(/_/g, "/");
  const raw = atob(b64);
  const out = new Uint8Array(raw.length);
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i);
  return out;
}

/** Cache the raw VAPID public key so the SW can re-subscribe without the app. */
export async function cacheVapidKey(base64: string): Promise<void> {
  if (!("caches" in self)) return;
  const cache = await caches.open("silo-push");
  await cache.put("vapid-public-key", new Response(base64));
}

export function pushSupported(): boolean {
  return (
    typeof navigator !== "undefined" &&
    "serviceWorker" in navigator &&
    typeof window !== "undefined" &&
    "PushManager" in window &&
    "Notification" in window
  );
}
