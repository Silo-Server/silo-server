import { createContext, useEffect } from "react";
import type { ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";

import { api } from "@/api/client";
import { themeKeys } from "@/hooks/queries/keys";
import { setAppDocumentTitle } from "@/lib/documentTitle";

const DEFAULT_SERVER_NAME = "Silo";
const DEFAULT_LOGIN_SUBTITLE = "Sign in with an existing account.";

/** Raw shape of GET /theme/branding. All fields optional / additive. */
interface BrandingApiResponse {
  server_name?: string;
  login_subtitle?: string;
  accent_color?: string;
  default_theme?: string;
  wordmark_url?: string;
  mark_url?: string;
  favicon_url?: string;
  login_bg_url?: string;
  storage_available?: boolean;
}

export interface BrandingContextValue {
  serverName: string;
  loginSubtitle: string;
  /** Brand accent color (hex), or null when unset. */
  accentColor: string | null;
  /** Admin-selected default theme id, or null when unset. */
  defaultTheme: string | null;
  /** Custom asset URLs (stable, cache-busted) or null to use bundled defaults. */
  wordmarkUrl: string | null;
  markUrl: string | null;
  faviconUrl: string | null;
  loginBgUrl: string | null;
  /** Whether the running server has an active object-store client for assets. */
  storageAvailable: boolean;
}

const DEFAULT_BRANDING: BrandingContextValue = {
  serverName: DEFAULT_SERVER_NAME,
  loginSubtitle: DEFAULT_LOGIN_SUBTITLE,
  accentColor: null,
  defaultTheme: null,
  wordmarkUrl: null,
  markUrl: null,
  faviconUrl: null,
  loginBgUrl: null,
  storageAvailable: false,
};

// A non-null default means useBranding() is safe to call anywhere (e.g. in
// SiloBrand rendered outside the provider in tests) and simply yields defaults.
export const BrandingContext = createContext<BrandingContextValue>(DEFAULT_BRANDING);

function mapResponse(data: BrandingApiResponse | undefined): BrandingContextValue {
  return {
    serverName: data?.server_name || DEFAULT_SERVER_NAME,
    loginSubtitle: data?.login_subtitle || DEFAULT_LOGIN_SUBTITLE,
    accentColor: data?.accent_color || null,
    defaultTheme: data?.default_theme || null,
    wordmarkUrl: data?.wordmark_url || null,
    markUrl: data?.mark_url || null,
    faviconUrl: data?.favicon_url || null,
    loginBgUrl: data?.login_bg_url || null,
    storageAvailable: data?.storage_available ?? false,
  };
}

/** Updates (or restores) the favicon <link> to the custom asset URL. */
function applyFavicon(url: string | null) {
  if (typeof document === "undefined") return;
  let link = document.querySelector<HTMLLinkElement>('link[rel="icon"]');
  if (!link) {
    link = document.createElement("link");
    link.rel = "icon";
    document.head.appendChild(link);
  }
  link.href = url ?? "/favicon.ico";
}

/**
 * BrandingProvider fetches server branding (public endpoint, available before
 * login) and exposes it via context. It owns the document-title and favicon
 * side effects so they run exactly once, fixing the tab title and applying the
 * custom favicon in dev (where the server does not template index.html).
 */
export function BrandingProvider({ children }: { children: ReactNode }) {
  // Errors propagate so TanStack Query retries transient failures; until data
  // arrives (or if it ultimately fails) `data` is undefined and mapResponse
  // falls back to defaults, so the UI degrades gracefully without swallowing
  // the error state.
  const { data } = useQuery({
    queryKey: themeKeys.branding(),
    queryFn: () => api<BrandingApiResponse>("/theme/branding"),
    staleTime: 5 * 60_000,
  });

  const value = mapResponse(data);

  useEffect(() => {
    setAppDocumentTitle(value.serverName);
  }, [value.serverName]);

  useEffect(() => {
    applyFavicon(value.faviconUrl);
  }, [value.faviconUrl]);

  return <BrandingContext value={value}>{children}</BrandingContext>;
}
