import { createContext, useContext } from "react";
import type { ReactNode } from "react";

/**
 * PlayerConfig is the portability contract — the host app provides these
 * values so the player module never imports app-specific code.
 */
export interface PlayerConfig {
  /** Base URL for API calls, e.g. "/api/v2" */
  apiBaseUrl: string;
  /** Sync getter for the current JWT access token. */
  getAccessToken: () => string | null;
  /** Sync getter for the current profile ID. */
  getProfileId: () => string | null;
  /** Sync getter for the current verified profile token, when one exists. */
  getProfileToken?: () => string | null;
  /**
   * The device headers every player request carries: at least the stable
   * `X-Silo-Device-Id` used for device-scoped playback settings, plus the
   * device name and platform the server records on the session.
   */
  getDeviceHeaders: () => Record<string, string>;
  /** Optional async token refresh callback on 401 response. */
  refreshToken?: () => Promise<boolean>;
  /** Account/server generation; unchanged by automatic token rotation. */
  getAuthContext?: () => unknown;
  /**
   * Runs once the stop a player sends as it unmounts has settled, delivered
   * or given up. The stop can write more than the last progress report (watch
   * history, the later episodes of a multi-episode file), so pages that show
   * watch state read it again here.
   */
  onSessionStopped?: () => void;
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
