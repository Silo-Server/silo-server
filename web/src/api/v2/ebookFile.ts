import {
  API_BLOB_MAX_BYTES,
  fetchWithSession,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
  type ProfileRequestContextSnapshot,
} from "@/api/client";
import { decodeV2Response, V2_CLIENT_HEADERS, V2TransportError } from "./request";

/** Binary reader boundary: authenticated v2 bytes, never JSON success decoding. */
export async function readEbookBlob(
  contentID: string,
  fileID: number,
  profileContext: ProfileRequestContextSnapshot | null,
  signal?: AbortSignal,
): Promise<Blob> {
  if (!profileContext) throw new StaleApiRequestContextError();
  const { res } = await fetchWithSession(
    `/api/v2/ebooks/${encodeURIComponent(contentID)}/files/${encodeURIComponent(String(fileID))}/read`,
    {
      method: "GET",
      signal,
      headers: {
        ...V2_CLIENT_HEADERS,
        Authorization: `Bearer ${profileContext.accessToken}`,
        "X-Profile-Id": profileContext.profileId,
        "X-Profile-Token": profileContext.profileToken ?? "",
      },
    },
    profileContext,
  );
  if (!res.ok) {
    await decodeV2Response("GET /api/v2/ebooks/{content_id}/files/{file_id}/read", res);
    throw new V2TransportError("readEbookFile", res.status, "unexpected file response");
  }
  const contentLength = Number(res.headers.get("Content-Length"));
  if (Number.isFinite(contentLength) && contentLength > API_BLOB_MAX_BYTES) {
    await res.body?.cancel();
    throw new Error("This file is too large to open in the browser. Download it instead.");
  }
  const blob = await res.blob();
  if (!isCapturedProfileAuthorityActive(profileContext)) throw new StaleApiRequestContextError();
  return blob;
}
