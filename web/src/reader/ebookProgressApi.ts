import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
  type ProfileRequestContextSnapshot,
} from "@/api/client";
import { v2, type V2Body } from "@/api/v2/request";
import type { components } from "@/api/v2/schema";

export type EbookReaderProgressPayload = {
  file_id: number;
  location: string;
  progress: number;
};

export type EbookReaderProgress = EbookReaderProgressPayload & {
  content_id?: string;
  updated_at?: string;
};

export type EbookProgressIntent = {
  body: V2Body<"PUT /api/v2/ebooks/{content_id}/progress">;
  profileContext: ProfileRequestContextSnapshot;
};

/** Capture the event time and authority before debounce or page teardown. */
export function captureEbookProgressIntent(
  progress: EbookReaderProgressPayload,
  profileContext = captureProfileRequestContext(),
): EbookProgressIntent | null {
  if (!profileContext) return null;
  return {
    body: { ...progress, file_id: String(progress.file_id), updated_at: new Date().toISOString() },
    profileContext,
  };
}

function progressFromV2(
  progress: components["schemas"]["EbookProgress"] | undefined,
): EbookReaderProgress | null {
  if (!progress || !progress.location.trim()) return null;
  const fileID = Number(progress.file_id);
  if (!Number.isSafeInteger(fileID) || fileID <= 0) {
    throw new Error("The reader cannot represent this file identifier.");
  }
  return { ...progress, file_id: fileID };
}

export async function fetchEbookReaderProgress(
  contentID: string,
): Promise<EbookReaderProgress | null> {
  const profileContext = captureProfileRequestContext();
  if (!profileContext) return null;
  const result = await v2("GET /api/v2/ebooks/{content_id}/progress", {
    path: { content_id: contentID },
    profileContext,
  });
  if (!isCapturedProfileAuthorityActive(profileContext)) throw new StaleApiRequestContextError();
  return progressFromV2(result.progress);
}

export async function saveEbookReaderProgress(
  contentID: string,
  intent: EbookProgressIntent,
  keepalive = false,
): Promise<EbookReaderProgress> {
  const result = await v2("PUT /api/v2/ebooks/{content_id}/progress", {
    path: { content_id: contentID },
    body: intent.body,
    profileContext: intent.profileContext,
    keepalive,
    retryAuthentication: !keepalive,
  });
  const progress = progressFromV2(result.progress);
  if (!progress) throw new Error("The reader save returned no position.");
  return progress;
}
