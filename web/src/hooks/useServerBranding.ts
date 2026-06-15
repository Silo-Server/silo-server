import { useBranding } from "@/hooks/useBranding";

interface ServerBranding {
  serverName: string;
  loginSubtitle: string;
}

/**
 * Backward-compatible accessor for the server name and login subtitle. Branding
 * is now fetched once by BrandingProvider; this delegates to that context.
 *
 * @deprecated Prefer useBranding() for access to the full branding set.
 */
export function useServerBranding(): ServerBranding {
  const { serverName, loginSubtitle } = useBranding();
  return { serverName, loginSubtitle };
}
