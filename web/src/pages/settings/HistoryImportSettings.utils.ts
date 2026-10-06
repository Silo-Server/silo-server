import type { EmbyConnectLoginResponse, HistoryImportSource } from "@/api/types";

export type SourceType = "emby" | "jellyfin" | "plex";
export type EmbyMode = "connect" | "saved";
export type PlexMode = "oauth" | "saved";

export function canStartEmbyImport(
  mode: EmbyMode,
  profileId: string,
  connectSession: EmbyConnectLoginResponse | null,
  connectServerId: string,
  selectedSavedSource: HistoryImportSource | undefined,
  savedUsername: string,
): boolean {
  if (!profileId) return false;
  if (mode === "connect") return !!connectSession?.connect_session_id && !!connectServerId;
  // Emby accounts may have no password, so only the username is required.
  return !!selectedSavedSource && savedUsername.trim() !== "";
}

export function canStartJellyfinImport(
  profileId: string,
  serverURL: string,
  username: string,
  password: string,
): boolean {
  return !!profileId && !!serverURL && !!username && !!password;
}
