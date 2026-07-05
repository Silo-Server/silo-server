import { api } from "@/api/client";

export type GuestControlPolicy = "host_only" | "guest_play_pause";
export type WatchTogetherRole = "host" | "guest";
export type WatchTogetherRoomPhase = "lobby" | "playing" | "ended";
export type WatchTogetherPlaybackState = "idle" | "waiting" | "paused" | "playing";
export type WatchTogetherSelectionMode = "host_pick" | "vote";
export type WatchTogetherTransportAction = "play" | "pause" | "seek";

export interface WatchTogetherRoomMember {
  user_id: number;
  profile_id: string;
  display_name: string;
  is_host: boolean;
  is_self: boolean;
  connected: boolean;
}

export interface WatchTogetherRoomSnapshot {
  room_id: string;
  phase: WatchTogetherRoomPhase;
  playback_state: WatchTogetherPlaybackState;
  selection_mode: WatchTogetherSelectionMode;
  selection_revision: number;
  selected_content_id?: string;
  selected_file_id?: number;
  selected_library_id?: number;
  code: string;
  guest_control_policy: GuestControlPolicy;
  is_paused: boolean;
  anchor_position_seconds: number;
  anchor_updated_at: string;
  generation: number;
  member_count: number;
  host_connected: boolean;
  self_role: WatchTogetherRole;
  self_can_control_transport: boolean;
  self_can_manage_room: boolean;
  self_ignore_wait: boolean;
  attached_session_id?: string;
  invite_path?: string;
  /** Optional additive field; absent on older servers. Host is listed first. */
  members?: WatchTogetherRoomMember[];
}

export interface WatchTogetherTransportCommand {
  command_id: string;
  session_id?: string;
  selection_revision: number;
  action: WatchTogetherTransportAction;
  position_seconds: number;
  execute_at: string;
  issued_at: string;
  playback_state: WatchTogetherPlaybackState;
}

export interface WatchTogetherRoomResponse {
  room: WatchTogetherRoomSnapshot;
  room_access_token?: string;
}

export interface CreateWatchTogetherRoomInput {
  file_id?: number;
  library_id?: number;
  selection_mode?: WatchTogetherSelectionMode;
}

export interface WatchTogetherSuggestion {
  id: string;
  room_id: string;
  suggester_user_id: number;
  suggester_profile_id: string;
  content_id: string;
  content_type: "movie" | "episode";
  title: string;
  subtitle: string;
  poster_url: string;
  note: string;
  vote_count: number;
  voted_by_me: boolean;
  created_at: string;
}

export interface WatchTogetherSuggestionsResponse {
  suggestions: WatchTogetherSuggestion[];
}

export interface CreateWatchTogetherSuggestionInput {
  content_id: string;
  content_type: "movie" | "episode";
  title: string;
  subtitle?: string;
  poster_url?: string;
  note?: string;
}

export interface JoinWatchTogetherRoomInput {
  code?: string;
  join_token?: string;
}

export interface SelectWatchTogetherRoomItemInput {
  content_id: string;
  file_id?: number;
  library_id?: number;
}

export async function createWatchTogetherRoom(input: CreateWatchTogetherRoomInput = {}) {
  return api<WatchTogetherRoomResponse>("/watch-together/rooms", {
    method: "POST",
    body: JSON.stringify(input),
  });
}

export async function joinWatchTogetherRoom(input: JoinWatchTogetherRoomInput) {
  return api<WatchTogetherRoomResponse>("/watch-together/join", {
    method: "POST",
    body: JSON.stringify(input),
  });
}

export async function getWatchTogetherRoom(roomId: string, roomToken: string) {
  const params = new URLSearchParams({ room_token: roomToken });
  return api<WatchTogetherRoomResponse>(`/watch-together/rooms/${roomId}?${params.toString()}`);
}

export async function updateWatchTogetherRoomPolicy(
  roomId: string,
  guestControlPolicy: GuestControlPolicy,
) {
  return api<WatchTogetherRoomResponse>(`/watch-together/rooms/${roomId}/policy`, {
    method: "PATCH",
    body: JSON.stringify({ guest_control_policy: guestControlPolicy }),
  });
}

export async function selectWatchTogetherRoomItem(
  roomId: string,
  input: SelectWatchTogetherRoomItemInput,
) {
  return api<WatchTogetherRoomResponse>(`/watch-together/rooms/${roomId}/selection`, {
    method: "PUT",
    body: JSON.stringify(input),
  });
}

export async function closeWatchTogetherRoom(roomId: string) {
  return api<void>(`/watch-together/rooms/${roomId}`, {
    method: "DELETE",
  });
}

export async function listWatchTogetherSuggestions(roomId: string, roomToken: string) {
  const params = new URLSearchParams({ room_token: roomToken });
  return api<WatchTogetherSuggestionsResponse>(
    `/watch-together/rooms/${roomId}/suggestions?${params.toString()}`,
  );
}

export async function createWatchTogetherSuggestion(
  roomId: string,
  roomToken: string,
  input: CreateWatchTogetherSuggestionInput,
) {
  const params = new URLSearchParams({ room_token: roomToken });
  return api<WatchTogetherSuggestionsResponse>(
    `/watch-together/rooms/${roomId}/suggestions?${params.toString()}`,
    {
      method: "POST",
      body: JSON.stringify(input),
    },
  );
}

export async function deleteWatchTogetherSuggestion(
  roomId: string,
  roomToken: string,
  suggestionId: string,
) {
  const params = new URLSearchParams({ room_token: roomToken });
  return api<WatchTogetherSuggestionsResponse>(
    `/watch-together/rooms/${roomId}/suggestions/${suggestionId}?${params.toString()}`,
    {
      method: "DELETE",
    },
  );
}

export async function voteWatchTogetherSuggestion(
  roomId: string,
  roomToken: string,
  suggestionId: string,
) {
  const params = new URLSearchParams({ room_token: roomToken });
  return api<WatchTogetherSuggestionsResponse>(
    `/watch-together/rooms/${roomId}/suggestions/${suggestionId}/vote?${params.toString()}`,
    {
      method: "POST",
    },
  );
}

export async function unvoteWatchTogetherSuggestion(
  roomId: string,
  roomToken: string,
  suggestionId: string,
) {
  const params = new URLSearchParams({ room_token: roomToken });
  return api<WatchTogetherSuggestionsResponse>(
    `/watch-together/rooms/${roomId}/suggestions/${suggestionId}/vote?${params.toString()}`,
    {
      method: "DELETE",
    },
  );
}

export async function promoteWatchTogetherSuggestion(
  roomId: string,
  roomToken: string,
  suggestionId: string,
) {
  const params = new URLSearchParams({ room_token: roomToken });
  return api<WatchTogetherRoomResponse>(
    `/watch-together/rooms/${roomId}/suggestions/promote?${params.toString()}`,
    {
      method: "POST",
      body: JSON.stringify({ suggestion_id: suggestionId }),
    },
  );
}

export function buildWatchTogetherInviteUrl(invitePath?: string | null) {
  if (!invitePath) {
    return null;
  }
  if (typeof window === "undefined") {
    return invitePath;
  }
  return new URL(invitePath, window.location.origin).toString();
}
