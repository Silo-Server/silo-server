import { useBranding } from "@/hooks/useBranding";
import { useT } from "@/i18n";

interface ServerBranding {
  serverName: string;
  loginSubtitle: string;
}

/**
 * Backward-compatible accessor for the server name and login subtitle. Branding
 * is now fetched once by BrandingProvider; this delegates to that context. A
 * subtitle the administrator set shows as typed; the default is translated.
 *
 * @deprecated Prefer useBranding() for access to the full branding set.
 */
export function useServerBranding(): ServerBranding {
  const t = useT();
  const { serverName, loginSubtitle } = useBranding();
  return { serverName, loginSubtitle: loginSubtitle || t("web.auth.loginSubtitle.default") };
}
