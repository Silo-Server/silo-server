import {
  fetchWithSession,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
  type ProfileRequestContextSnapshot,
} from "../client";
import { decodeV2Response, v2, V2_CLIENT_HEADERS, V2ProblemError, type V2Result } from "./request";

const MIN_UPLOAD_CHUNK_SIZE = 128 * 1024;
const DEFAULT_UPLOAD_CHUNK_SIZE = 512 * 1024;

export interface ChunkedUploadProgress {
  uploadId: string;
  uploadedBytes: number;
  totalBytes: number;
  uploadedChunks: number;
  totalChunks: number;
  percent: number;
}

export type AdminPluginUploadResult = V2Result<"POST /api/v2/admin/plugins/uploads">;
type UploadSession = V2Result<"POST /api/v2/admin/plugins/uploads/chunked">;

export interface UploadAdminPluginOptions {
  file: File;
  /** Authority captured when the operator chose the file; every request carries exactly it. */
  profileContext: ProfileRequestContextSnapshot;
  chunkSize?: number;
  onProgress?: (progress: ChunkedUploadProgress) => void;
}

/**
 * Installs one plugin archive through the v2 upload operations under captured
 * authority and with no automatic or authentication replay: a small file goes
 * as one multipart request; a larger one opens a process-local chunked
 * session, PUTs each chunk once as application/octet-stream, then completes.
 * A 413 on the first chunk halves the chunk size and starts a new session.
 * The session is cancelled on any failure; the original failure is preserved.
 */
export async function uploadAdminPlugin(
  options: UploadAdminPluginOptions,
): Promise<AdminPluginUploadResult> {
  const { file, profileContext } = options;
  if (!isCapturedProfileAuthorityActive(profileContext)) throw new StaleApiRequestContextError();
  if (file.size <= DEFAULT_UPLOAD_CHUNK_SIZE) {
    return v2("POST /api/v2/admin/plugins/uploads", {
      form: { archive: file },
      profileContext,
      retryAuthentication: false,
    });
  }
  let chunkSize = options.chunkSize ?? DEFAULT_UPLOAD_CHUNK_SIZE;
  for (;;) {
    try {
      return await uploadInChunks({ ...options, chunkSize });
    } catch (error) {
      if (
        !(error instanceof V2ProblemError && error.status === 413) ||
        chunkSize <= MIN_UPLOAD_CHUNK_SIZE
      )
        throw error;
      chunkSize = Math.max(MIN_UPLOAD_CHUNK_SIZE, Math.floor(chunkSize / 2));
    }
  }
}

async function uploadInChunks({
  file,
  profileContext,
  chunkSize,
  onProgress,
}: UploadAdminPluginOptions & { chunkSize: number }): Promise<AdminPluginUploadResult> {
  const common = { profileContext, retryAuthentication: false };
  let uploadId: string | null = null;
  try {
    const session = await v2("POST /api/v2/admin/plugins/uploads/chunked", {
      ...common,
      body: { filename: file.name, size_bytes: file.size, chunk_size: chunkSize },
    });
    uploadId = session.upload_id;
    report(session, file.size, onProgress);
    for (let index = session.received_chunks; index < session.total_chunks; index += 1) {
      const start = index * session.chunk_size;
      const end = Math.min(start + session.chunk_size, file.size);
      const progress = await putChunk(uploadId, index, file.slice(start, end), profileContext);
      report(progress, file.size, onProgress);
    }
    const installed = await v2("POST /api/v2/admin/plugins/uploads/chunked/{upload_id}/complete", {
      ...common,
      path: { upload_id: uploadId },
    });
    uploadId = null;
    return installed;
  } catch (error) {
    if (uploadId && isCapturedProfileAuthorityActive(profileContext)) {
      try {
        await v2("DELETE /api/v2/admin/plugins/uploads/chunked/{upload_id}", {
          ...common,
          path: { upload_id: uploadId },
        });
      } catch {
        // Best-effort cleanup; the session expires on its own.
      }
    }
    throw error;
  }
}

/** One octet-stream chunk through the same captured session boundary as v2 JSON. */
async function putChunk(
  uploadId: string,
  index: number,
  chunk: Blob,
  profileContext: ProfileRequestContextSnapshot,
): Promise<UploadSession> {
  if (!isCapturedProfileAuthorityActive(profileContext)) throw new StaleApiRequestContextError();
  const key = "PUT /api/v2/admin/plugins/uploads/chunked/{upload_id}/chunks/{chunk_index}" as const;
  const { res } = await fetchWithSession(
    `/api/v2/admin/plugins/uploads/chunked/${encodeURIComponent(uploadId)}/chunks/${index}`,
    {
      method: "PUT",
      headers: {
        ...V2_CLIENT_HEADERS,
        Accept: "application/json",
        "Content-Type": "application/octet-stream",
        Authorization: `Bearer ${profileContext.accessToken}`,
        "X-Profile-Id": profileContext.profileId,
        "X-Profile-Token": profileContext.profileToken ?? "",
      },
      body: chunk,
    },
    profileContext,
    false,
  );
  const decoded = await decodeV2Response(key, res);
  if (!isCapturedProfileAuthorityActive(profileContext)) throw new StaleApiRequestContextError();
  return decoded;
}

function report(
  session: UploadSession,
  fallbackTotalBytes: number,
  onProgress?: (progress: ChunkedUploadProgress) => void,
) {
  if (!onProgress) return;
  const totalBytes = session.size_bytes || fallbackTotalBytes;
  const uploadedBytes = Math.min(session.received_bytes, totalBytes);
  const percent = totalBytes > 0 ? Math.round((uploadedBytes / totalBytes) * 100) : 0;
  onProgress({
    uploadId: session.upload_id,
    uploadedBytes,
    totalBytes,
    uploadedChunks: session.received_chunks,
    totalChunks: session.total_chunks,
    percent: Math.min(100, Math.max(0, percent)),
  });
}
