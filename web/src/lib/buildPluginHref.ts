import {
  captureProfileRequestContext,
  captureSessionIdentity,
  isCapturedProfileAuthorityActive,
  isSessionIdentityCurrent,
} from "@/api/client";
import { v2 } from "@/api/v2/request";
import { toast } from "sonner";

// Full-page plugin navigation needs the HttpOnly cookie scoped to the common
// v2 plugin-content parent, so both pages and assets receive it.
export const PLUGIN_LAUNCH_PATH = "/api/v2/auth/plugin-launch";

export function buildPluginHref(basePath: string): string {
  const theme = document.documentElement.dataset.theme;
  const params = new URLSearchParams();
  if (theme) params.set("theme", theme);
  const qs = params.toString();
  if (!qs) return basePath;
  const sep = basePath.includes("?") ? "&" : "?";
  return `${basePath}${sep}${qs}`;
}

export async function navigateToPluginRoute(basePath: string): Promise<void> {
  const profileContext = captureProfileRequestContext();
  const sessionIdentity = captureSessionIdentity();
  const authorityActive = () =>
    profileContext
      ? isCapturedProfileAuthorityActive(profileContext)
      : isSessionIdentityCurrent(sessionIdentity) && captureProfileRequestContext() === null;
  try {
    // Launch also supports a login session before a household profile is selected.
    await v2("POST /api/v2/auth/plugin-launch", { profileContext: profileContext ?? undefined });
    if (!authorityActive()) return;
    window.location.href = buildPluginHref(basePath);
  } catch {
    if (authorityActive()) {
      toast.error("Unable to open the plugin. Please try again.");
    }
  }
}
