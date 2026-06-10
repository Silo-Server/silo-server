// web/public/sw.js — static service worker for Web Push.
// NOTE: the buildNotification/clickUrl logic mirrors src/lib/pushSw.ts (the
// tested source of truth). Keep them in sync.

function buildNotification(payload) {
  const p = payload || {};
  const link = p.link && p.link.length > 0 ? p.link : "/notifications";
  const tag = p.id != null ? "n" + p.id : "n";
  return {
    title: p.title && p.title.length > 0 ? p.title : "Silo",
    options: { body: p.body || "", data: { link: link }, tag: tag },
  };
}
function clickUrl(data) {
  const d = data || {};
  return d.link && d.link.length > 0 ? d.link : "/notifications";
}

self.addEventListener("push", (event) => {
  let payload = null;
  try {
    payload = event.data ? event.data.json() : null;
  } catch (e) {
    payload = null;
  }
  const n = buildNotification(payload);
  event.waitUntil(self.registration.showNotification(n.title, n.options));
});

self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  const url = clickUrl(event.notification.data);
  event.waitUntil(
    self.clients.matchAll({ type: "window", includeUncontrolled: true }).then((wins) => {
      for (const c of wins) {
        if ("focus" in c) {
          c.postMessage({ type: "notification-click", link: url });
          return c.focus();
        }
      }
      return self.clients.openWindow(url);
    }),
  );
});

self.addEventListener("pushsubscriptionchange", (event) => {
  // Best-effort re-subscribe using the cached VAPID key; the app re-registers
  // the new subscription on next load if this fails.
  event.waitUntil(
    (async () => {
      try {
        const cache = await caches.open("silo-push");
        const res = await cache.match("vapid-public-key");
        if (!res) return;
        const vapid = await res.text();
        const sub = await self.registration.pushManager.subscribe({
          userVisibleOnly: true,
          applicationServerKey: vapid,
        });
        await fetch("/api/v1/notifications/push/device", {
          method: "PUT",
          headers: { "Content-Type": "application/json" },
          credentials: "include",
          body: JSON.stringify({ transport: "webpush", token: JSON.stringify(sub.toJSON()) }),
        });
      } catch (e) {
        // swallow; app re-registers on next load
      }
    })(),
  );
});
