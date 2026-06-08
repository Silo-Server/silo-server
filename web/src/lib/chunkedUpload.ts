import { ApiClientError, api } from "@/api/client";

export const DEFAULT_UPLOAD_CHUNK_SIZE = 512 * 1024;
const MIN_UPLOAD_CHUNK_SIZE = 128 * 1024;

export interface ChunkedUploadSession {
  upload_id: string;
  filename: string;
  size_bytes: number;
  chunk_size: number;
  total_chunks: number;
  received_chunks: number;
  received_bytes: number;
  complete: boolean;
  expires_at: string;
}

export interface ChunkedUploadProgress {
  uploadId: string;
  uploadedBytes: number;
  totalBytes: number;
  uploadedChunks: number;
  totalChunks: number;
  percent: number;
}

export interface UploadFileInChunksOptions {
  file: File;
  createPath: string;
  chunkPath: (uploadId: string, chunkIndex: number) => string;
  completePath: (uploadId: string) => string;
  cancelPath?: (uploadId: string) => string;
  chunkSize?: number;
  onProgress?: (progress: ChunkedUploadProgress) => void;
}

export async function uploadFileInChunks<TComplete>({
  file,
  createPath,
  chunkPath,
  completePath,
  cancelPath,
  chunkSize = DEFAULT_UPLOAD_CHUNK_SIZE,
  onProgress,
}: UploadFileInChunksOptions): Promise<TComplete> {
  let nextChunkSize = chunkSize;

  for (;;) {
    try {
      return await uploadFileInChunksAttempt<TComplete>({
        file,
        createPath,
        chunkPath,
        completePath,
        cancelPath,
        chunkSize: nextChunkSize,
        onProgress,
      });
    } catch (error) {
      if (!isRequestTooLarge(error) || nextChunkSize <= MIN_UPLOAD_CHUNK_SIZE) {
        throw error;
      }
      nextChunkSize = Math.max(MIN_UPLOAD_CHUNK_SIZE, Math.floor(nextChunkSize / 2));
    }
  }
}

type UploadAttemptOptions = Omit<UploadFileInChunksOptions, "chunkSize"> & {
  chunkSize: number;
};

async function uploadFileInChunksAttempt<TComplete>({
  file,
  createPath,
  chunkPath,
  completePath,
  cancelPath,
  chunkSize,
  onProgress,
}: UploadAttemptOptions): Promise<TComplete> {
  let uploadId: string | null = null;

  try {
    const session = await api<ChunkedUploadSession>(createPath, {
      method: "POST",
      body: JSON.stringify({
        filename: file.name,
        size_bytes: file.size,
        chunk_size: chunkSize,
      }),
    });
    uploadId = session.upload_id;

    reportProgress(session, file.size, onProgress);

    for (let index = session.received_chunks; index < session.total_chunks; index += 1) {
      const start = index * session.chunk_size;
      const end = Math.min(start + session.chunk_size, file.size);

      const progress = await api<ChunkedUploadSession>(chunkPath(session.upload_id, index), {
        method: "PUT",
        headers: { "Content-Type": "application/octet-stream" },
        body: file.slice(start, end),
      });

      reportProgress(progress, file.size, onProgress);
    }

    return api<TComplete>(completePath(session.upload_id), { method: "POST" });
  } catch (error) {
    if (uploadId && cancelPath) {
      try {
        await api(cancelPath(uploadId), { method: "DELETE" });
      } catch {
        // Best effort cleanup; preserve the original upload failure.
      }
    }
    throw error;
  }
}

function isRequestTooLarge(error: unknown): boolean {
  return error instanceof ApiClientError && error.status === 413;
}

function reportProgress(
  session: ChunkedUploadSession,
  fallbackTotalBytes: number,
  onProgress?: (progress: ChunkedUploadProgress) => void,
) {
  if (!onProgress) {
    return;
  }

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
