import { createContext, useContext } from "react";
import type { ReactNode } from "react";

/**
 * PlayerConfig is the portability contract — the host app provides these
 * values so the player module never imports app-specific code.
 */
export interface PlayerConfig {
  /** Base URL for API calls, e.g. "/api/v1" */
  apiBaseUrl: string;
  /** Sync getter for the current JWT access token. */
  getAccessToken: () => string | null;
  /** Sync getter for the current profile ID. */
  getProfileId: () => string | null;
  /** Sync getter for the current verified profile token, when one exists. */
  getProfileToken?: () => string | null;
  /** Stable device identity used for device-scoped playback settings. */
  getDeviceId: () => string;
}

const PlayerConfigCtx = createContext<PlayerConfig | null>(null);

export function PlayerConfigProvider({
  config,
  children,
}: {
  config: PlayerConfig;
  children: ReactNode;
}) {
  return <PlayerConfigCtx.Provider value={config}>{children}</PlayerConfigCtx.Provider>;
}

export function usePlayerConfig(): PlayerConfig {
  const ctx = useContext(PlayerConfigCtx);
  if (!ctx) throw new Error("usePlayerConfig must be used within PlayerConfigProvider");
  return ctx;
}
