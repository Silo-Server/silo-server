/**
 * Silo service worker: displays Web Push notifications and routes clicks.
 * Payloads arrive end-to-end encrypted (RFC 8291); by the time the push
 * event fires the browser has decrypted them for us.
 */

self.addEventListener("install", () => {
  self.skipWaiting();
});

self.addEventListener("activate", (event) => {
  event.waitUntil(self.clients.claim());
});

self.addEventListener("push", (event) => {
  let data = {};
  try {
    data = event.data ? event.data.json() : {};
  } catch {
    data = {};
  }
  const title = data.title || "Silo";
  const options = {
    body: data.body || "",
    icon: data.icon || "/web-app-icon-192.png",
    badge: "/web-app-icon-192.png",
    tag: data.tag || undefined,
    data: { url: data.url || "/notifications" },
  };
  event.waitUntil(self.registration.showNotification(title, options));
});

self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  // Notifications navigate same-origin only: payload data is server-built,
  // but a notification surface must never become an open redirect.
  const rawUrl = (event.notification.data && event.notification.data.url) || "/notifications";
  let url = "/notifications";
  try {
    const parsed = new URL(rawUrl, self.location.origin);
    if (parsed.origin === self.location.origin) {
      url = `${parsed.pathname}${parsed.search}${parsed.hash}`;
    }
  } catch {
    // keep the safe default
  }
  event.waitUntil(
    self.clients.matchAll({ type: "window", includeUncontrolled: true }).then((clientList) => {
      for (const client of clientList) {
        if ("focus" in client) {
          if ("navigate" in client) {
            client.navigate(url);
          }
          return client.focus();
        }
      }
      return self.clients.openWindow(url);
    }),
  );
});
