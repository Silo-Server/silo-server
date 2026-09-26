import { createRoom, type RoomCreationDraft } from "@/api/v2/watchTogetherCreate";
import { promoteRoomSuggestion } from "@/api/v2/watchTogetherSuggestionPromote";
import {
  createRoomSuggestion,
  type SuggestionCreationDraft,
} from "@/api/v2/watchTogetherSuggestionCreate";
import { joinRoom } from "@/api/v2/watchTogetherJoin";
import { selectRoomItem } from "@/api/v2/watchTogetherSelection";
import { updateRoomPolicy } from "@/api/v2/watchTogetherPolicy";
import { readRoom } from "@/api/v2/watchTogetherRoomRead";
import { deleteRoomSuggestion } from "@/api/v2/watchTogetherSuggestionDelete";
import { listRoomSuggestions, setRoomSuggestionVote } from "@/api/v2/watchTogetherSuggestions";
import { captureProfileRequestContext, type ProfileRequestContextSnapshot } from "@/api/client";
import { closeRoom } from "@/api/v2/watchTogetherClose";
import { stageRoomItem } from "@/api/v2/watchTogetherStage";
import { startRoomPlayback } from "@/api/v2/watchTogetherStart";
import { stopRoomPlayback } from "@/api/v2/watchTogetherStop";
import { updateRoomSelectionMode } from "@/api/v2/watchTogetherSelectionMode";
import { queryRoomMemberState } from "@/api/v2/watchTogetherMemberState";
import { readRoomPicker } from "@/api/v2/watchTogetherPicker";
import { readWatchTogetherCapabilities } from "@/api/v2/watchTogetherCapabilities";

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
  is_ready?: boolean;
  is_buffering?: boolean;
  is_syncing?: boolean;
  /** Lobby "I'm ready". Absent on older servers; always false once playing. */
  lobby_ready?: boolean;
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

export async function createWatchTogetherRoom(draft: RoomCreationDraft) {
  return createRoom(draft);
}

export async function joinWatchTogetherRoom(
  input: JoinWatchTogetherRoomInput,
  authority = captureProfileRequestContext(),
) {
  return joinRoom({ ...input }, authority);
}

export async function getWatchTogetherRoom(
  roomId: string,
  roomToken: string,
  authority = captureProfileRequestContext(),
) {
  return readRoom(roomId, roomToken, authority);
}

export async function updateWatchTogetherRoomPolicy(
  roomId: string,
  guestControlPolicy: GuestControlPolicy,
  authority = captureProfileRequestContext(),
) {
  return updateRoomPolicy(roomId, guestControlPolicy, authority);
}

/** A lobby with a selection is staged: chosen, not yet started. */
export function isRoomStaged(room: WatchTogetherRoomSnapshot | null | undefined): boolean {
  return !!room && room.phase === "lobby" && !!room.selected_content_id;
}

export async function stageWatchTogetherRoomItem(
  roomId: string,
  input: SelectWatchTogetherRoomItemInput,
  authority = captureProfileRequestContext(),
) {
  return stageRoomItem(roomId, { ...input }, authority);
}

export async function startWatchTogetherRoomPlayback(
  roomId: string,
  authority = captureProfileRequestContext(),
) {
  return startRoomPlayback(roomId, authority);
}

export async function stopWatchTogetherRoomPlayback(
  roomId: string,
  authority = captureProfileRequestContext(),
) {
  return stopRoomPlayback(roomId, authority);
}

export async function updateWatchTogetherRoomSelectionMode(
  roomId: string,
  mode: WatchTogetherSelectionMode,
  authority = captureProfileRequestContext(),
) {
  return updateRoomSelectionMode(roomId, mode, authority);
}

export async function queryWatchTogetherMemberState(
  roomId: string,
  roomToken: string,
  contentIds: string[],
  authority = captureProfileRequestContext(),
) {
  return queryRoomMemberState(roomId, roomToken, contentIds, authority);
}

export async function getWatchTogetherRoomPicker(
  roomId: string,
  roomToken: string,
  authority = captureProfileRequestContext(),
) {
  return readRoomPicker(roomId, roomToken, authority);
}

export async function getWatchTogetherCapabilities() {
  return readWatchTogetherCapabilities();
}

export async function selectWatchTogetherRoomItem(
  roomId: string,
  input: SelectWatchTogetherRoomItemInput,
  authority = captureProfileRequestContext(),
) {
  return selectRoomItem(roomId, { ...input }, authority);
}

export async function closeWatchTogetherRoom(
  roomId: string,
  authority: ProfileRequestContextSnapshot | null = captureProfileRequestContext(),
) {
  return closeRoom(roomId, authority);
}

export async function listWatchTogetherSuggestions(roomId: string, roomToken: string) {
  return listRoomSuggestions(roomId, roomToken);
}

export async function createWatchTogetherSuggestion(draft: SuggestionCreationDraft) {
  return createRoomSuggestion(draft);
}

export async function deleteWatchTogetherSuggestion(
  roomId: string,
  roomToken: string,
  suggestionId: string,
) {
  return deleteRoomSuggestion(roomId, roomToken, suggestionId);
}

export async function voteWatchTogetherSuggestion(
  roomId: string,
  roomToken: string,
  suggestionId: string,
) {
  return setRoomSuggestionVote(roomId, roomToken, suggestionId, true);
}

export async function unvoteWatchTogetherSuggestion(
  roomId: string,
  roomToken: string,
  suggestionId: string,
) {
  return setRoomSuggestionVote(roomId, roomToken, suggestionId, false);
}

export async function promoteWatchTogetherSuggestion(
  roomId: string,
  roomToken: string,
  suggestionId: string,
  authority = captureProfileRequestContext(),
) {
  return promoteRoomSuggestion(roomId, roomToken, suggestionId, authority);
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
