import type { EmbyConnectLoginResponse, HistoryImportSource } from "@/api/types";

export type SourceType = "emby" | "jellyfin" | "plex";
export type EmbyMode = "connect" | "direct" | "saved";
export type PlexMode = "oauth" | "saved";

export function canStartEmbyImport(
  mode: EmbyMode,
  profileId: string,
  connectSession: EmbyConnectLoginResponse | null,
  connectServerId: string,
  selectedSavedSource: HistoryImportSource | undefined,
  savedUsername: string,
  directServerURL = "",
  directUsername = "",
): boolean {
  if (!profileId) return false;
  if (mode === "connect") return !!connectSession?.connect_session_id && !!connectServerId;
  if (mode === "direct") return directServerURL.trim() !== "" && directUsername.trim() !== "";
  // Emby accounts may have no password, so only the username is required.
  return !!selectedSavedSource && savedUsername.trim() !== "";
}

export function canStartJellyfinImport(
  profileId: string,
  serverURL: string,
  username: string,
  password: string,
): boolean {
  return !!profileId && serverURL.trim() !== "" && username.trim() !== "" && !!password;
}

export function isValidHttpServerURL(raw: string): boolean {
  const trimmed = raw.trim();
  if (!/^https?:\/\//i.test(trimmed) || trimmed.includes("?") || trimmed.includes("#")) {
    return false;
  }
  const authority = trimmed.slice(trimmed.indexOf("://") + 3).split("/")[0] ?? "";
  if (authority.includes("@")) return false;
  try {
    const parsed = new URL(trimmed);
    return (
      (parsed.protocol === "http:" || parsed.protocol === "https:") &&
      parsed.hostname !== "" &&
      parsed.username === "" &&
      parsed.password === "" &&
      parsed.search === "" &&
      parsed.hash === ""
    );
  } catch {
    return false;
  }
}
