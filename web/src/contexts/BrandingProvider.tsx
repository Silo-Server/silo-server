import { createContext, useEffect } from "react";
import type { ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";

import { v2 } from "@/api/v2/request";
import { themeKeys } from "@/hooks/queries/keys";
import { accentDeclarations } from "@/lib/accentColor";
import { setAppDocumentTitle } from "@/lib/documentTitle";

const DEFAULT_SERVER_NAME = "Silo";
const DEFAULT_LOGIN_SUBTITLE = "Sign in with an existing account.";

/** Raw shape of GET /theme/branding. All fields optional / additive. */
interface BrandingApiResponse {
  server_name?: string;
  login_subtitle?: string;
  accent_color?: string;
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
  /** Custom asset URLs (stable, cache-busted) or null to use bundled defaults. */
  wordmarkUrl: string | null;
  markUrl: string | null;
  faviconUrl: string | null;
  loginBgUrl: string | null;
  /** Whether the running server has an active object-store client for assets. */
  storageAvailable: boolean;
  /** The server's branding has arrived; until then every field is a default. */
  loaded: boolean;
}

const DEFAULT_BRANDING: BrandingContextValue = {
  serverName: DEFAULT_SERVER_NAME,
  loginSubtitle: DEFAULT_LOGIN_SUBTITLE,
  accentColor: null,
  wordmarkUrl: null,
  markUrl: null,
  faviconUrl: null,
  loginBgUrl: null,
  storageAvailable: false,
  loaded: false,
};

// A non-null default means useBranding() is safe to call anywhere (e.g. in
// SiloBrand rendered outside the provider in tests) and simply yields defaults.
export const BrandingContext = createContext<BrandingContextValue>(DEFAULT_BRANDING);

function mapResponse(data: BrandingApiResponse | undefined): BrandingContextValue {
  return {
    serverName: data?.server_name || DEFAULT_SERVER_NAME,
    loginSubtitle: data?.login_subtitle || DEFAULT_LOGIN_SUBTITLE,
    accentColor: data?.accent_color || null,
    wordmarkUrl: data?.wordmark_url || null,
    markUrl: data?.mark_url || null,
    faviconUrl: data?.favicon_url || null,
    loginBgUrl: data?.login_bg_url || null,
    storageAvailable: data?.storage_available ?? false,
    loaded: data !== undefined,
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

const ACCENT_STYLE_ID = "silo-accent";

/**
 * Paints the accent over Cinema Dark. It is an unlayered :root rule appended
 * to <head>, so it wins over the theme's layered tokens. That would also beat
 * high contrast's boosted focus ring, so a second unlayered rule restores it:
 * a dark accent must not hide keyboard focus for someone who asked for high
 * contrast.
 */
function applyAccent(hex: string | null) {
  if (typeof document === "undefined") return;
  const declarations = hex ? accentDeclarations(hex) : null;
  const css = declarations
    ? `:root {\n${Object.entries(declarations)
        .map(([token, value]) => `  --${token}: ${value};`)
        .join("\n")}\n}\nhtml[data-high-contrast="true"] {\n  --ring: var(--contrast-boost);\n}`
    : "";
  let style = document.getElementById(ACCENT_STYLE_ID) as HTMLStyleElement | null;
  if (!css) {
    style?.remove();
    return;
  }
  if (!style) {
    style = document.createElement("style");
    style.id = ACCENT_STYLE_ID;
    document.head.appendChild(style);
  }
  style.textContent = css;
}

/**
 * BrandingProvider fetches server branding (public endpoint, available before
 * login) and exposes it via context. It owns the document-title, favicon, and
 * accent side effects so they run exactly once, fixing the tab title and applying the
 * custom favicon in dev (where the server does not template index.html).
 */
export function BrandingProvider({ children }: { children: ReactNode }) {
  // Errors propagate so TanStack Query retries transient failures; until data
  // arrives (or if it ultimately fails) `data` is undefined and mapResponse
  // falls back to defaults, so the UI degrades gracefully without swallowing
  // the error state.
  const { data } = useQuery({
    queryKey: themeKeys.branding(),
    queryFn: () => v2("GET /api/v2/theme/branding"),
    staleTime: 5 * 60_000,
  });

  const value = mapResponse(data);

  useEffect(() => {
    setAppDocumentTitle(value.serverName);
  }, [value.serverName]);

  useEffect(() => {
    applyFavicon(value.faviconUrl);
  }, [value.faviconUrl]);

  useEffect(() => {
    applyAccent(value.accentColor);
  }, [value.accentColor]);

  return <BrandingContext value={value}>{children}</BrandingContext>;
}
