import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
} from "@/api/client";
import { normalizeRoomResponse } from "./watchTogetherRoomRead";
import { v2 } from "./request";

export type SourceFallbackReason =
  | "no_alternate_version"
  | "hdr_transcode_unsupported"
  | "subtitle_conversion_unsupported"
  | "transcoding_disabled";

export function isSourceFallbackReason(
  reason: string | null | undefined,
): reason is SourceFallbackReason {
  return (
    reason === "no_alternate_version" ||
    reason === "hdr_transcode_unsupported" ||
    reason === "subtitle_conversion_unsupported" ||
    reason === "transcoding_disabled"
  );
}

export interface SourceFallbackRequest {
  selectionRevision: number;
  failedFileId: number;
  reason: SourceFallbackReason;
}

export async function fallbackRoomSource(
  roomId: string,
  roomToken: string,
  input: SourceFallbackRequest,
  authority = captureProfileRequestContext(),
) {
  if (!authority || !isCapturedProfileAuthorityActive(authority))
    throw new StaleApiRequestContextError();
  if (
    !Number.isSafeInteger(input.failedFileId) ||
    input.failedFileId <= 0 ||
    !Number.isSafeInteger(input.selectionRevision) ||
    input.selectionRevision <= 0
  ) {
    throw new Error("Invalid source fallback identity.");
  }
  const result = await v2("POST /api/v2/watch-together/rooms/{room_id}/source-fallback", {
    path: { room_id: roomId },
    headers: { "X-Room-Token": roomToken },
    body: {
      selection_revision: input.selectionRevision,
      failed_file_id: String(input.failedFileId),
      reason: input.reason,
    },
    profileContext: authority,
    retryAuthentication: false,
  });
  if (!isCapturedProfileAuthorityActive(authority)) throw new StaleApiRequestContextError();
  return normalizeRoomResponse(roomId, result);
}
