import { api } from "@/api/client";

// Plugin SPAs run behind the plugin proxy, which authenticates each request
// via Authorization header or a short-lived HttpOnly launch cookie. A full-page
// anchor navigation can't add a header, so clicks prepare that cookie first.
// The launch endpoint, relative to the api() client's /api/v1 prefix. The
// cookie it sets is Path=/api/v1, the parent of every plugin SPA href.
export const PLUGIN_LAUNCH_PATH = "/auth/plugin-launch";

export function buildPluginHref(basePath: string): string {
  const theme = document.documentElement.dataset.theme;
  const params = new URLSearchParams();
  if (theme) params.set("theme", theme);
  const qs = params.toString();
  if (!qs) return basePath;
  const sep = basePath.includes("?") ? "&" : "?";
  return `${basePath}${sep}${qs}`;
}

// Click handler for any <a> linking to a plugin SPA: prevents default and
// prepares plugin access before navigation. Use this on every plugin link in
// the admin/user UI so behavior matches the sidebar.
//
// This stays on the v1 launch endpoint on purpose: plugin SPAs are served
// under /api/v1/plugins (pluginRouteHref.ts), and the cookie each endpoint
// issues is scoped to its own API prefix. v2 launchPlugin scopes it to
// /api/v2/plugins, which the browser would never send to the v1 page; move
// this call with the plugin proxy, not before it.
export async function navigateToPluginRoute(basePath: string): Promise<void> {
  try {
    await api<{ expires_in: number }>(PLUGIN_LAUNCH_PATH, { method: "POST" });
  } catch (error) {
    console.warn("plugin launch preparation failed", error);
  }
  window.location.href = buildPluginHref(basePath);
}
