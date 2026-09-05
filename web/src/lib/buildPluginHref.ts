import { v2 } from "@/api/v2/request";

// Plugin SPAs run behind the plugin proxy, which authenticates each request
// via Authorization header or a short-lived HttpOnly launch cookie. A full-page
// anchor navigation can't add a header, so clicks prepare that cookie first.
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
export async function navigateToPluginRoute(basePath: string): Promise<void> {
  try {
    await v2("POST /api/v2/auth/plugin-launch");
  } catch (error) {
    console.warn("plugin launch preparation failed", error);
  }
  window.location.href = buildPluginHref(basePath);
}
