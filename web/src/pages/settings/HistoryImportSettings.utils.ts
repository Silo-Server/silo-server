import type { EmbyConnectLoginResponse, HistoryImportSource, PlexCheckResponse } from "@/api/types";

export function resolveSavedSourceSelection(
  currentSavedSourceId: string,
  sources: HistoryImportSource[],
  lockedSavedSourceId: string,
) {
  if (currentSavedSourceId) {
    return {
      effectiveSavedSourceId: currentSavedSourceId,
      lockedSavedSourceId: currentSavedSourceId,
    };
  }

  if (lockedSavedSourceId) {
    return {
      effectiveSavedSourceId: lockedSavedSourceId,
      lockedSavedSourceId,
    };
  }

  const nextSavedSourceId = String(sources[0]?.id ?? "");
  return {
    effectiveSavedSourceId: nextSavedSourceId,
    lockedSavedSourceId: nextSavedSourceId,
  };
}

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

export function canStartPlexImport(
  mode: PlexMode,
  profileId: string,
  plexCheck: PlexCheckResponse | undefined,
  plexServerId: string,
  selectedSavedSource: HistoryImportSource | undefined,
  plexToken: string,
): boolean {
  if (!profileId) return false;
  if (mode === "oauth") return !!plexCheck?.authenticated && !!plexServerId;
  return !!selectedSavedSource && !!plexToken;
}
