export interface BulkDeleteResult {
  requested: number;
  deleted: number;
  kept: number;
  failed: number;
  firstError?: string;
}

export interface BulkDeleteProgress {
  completed: number;
  total: number;
}

type RejectedDeleteOutcome = "deleted" | "kept" | "failed";

const BULK_DELETE_BATCH_SIZE = 4;

export async function runBulkDelete(
  ids: string[],
  deleteOne: (id: string) => Promise<unknown>,
  classifyRejection: (error: unknown) => RejectedDeleteOutcome,
  onProgress: (progress: BulkDeleteProgress) => void,
): Promise<BulkDeleteResult> {
  const uniqueIds = [...new Set(ids)];
  let deleted = 0;
  let kept = 0;
  let failed = 0;
  let firstError: string | undefined;

  for (let offset = 0; offset < uniqueIds.length; offset += BULK_DELETE_BATCH_SIZE) {
    const batch = uniqueIds.slice(offset, offset + BULK_DELETE_BATCH_SIZE);
    const results = await Promise.allSettled(batch.map((id) => deleteOne(id)));

    for (const result of results) {
      if (result.status === "fulfilled") {
        deleted += 1;
        continue;
      }
      const outcome = classifyRejection(result.reason);
      if (outcome === "deleted") {
        deleted += 1;
      } else if (outcome === "kept") {
        kept += 1;
      } else {
        if (failed === 0 && result.reason instanceof Error) {
          firstError = result.reason.message;
        }
        failed += 1;
      }
    }

    onProgress({
      completed: Math.min(offset + batch.length, uniqueIds.length),
      total: uniqueIds.length,
    });
  }

  return {
    requested: uniqueIds.length,
    deleted,
    kept,
    failed,
    firstError,
  };
}
