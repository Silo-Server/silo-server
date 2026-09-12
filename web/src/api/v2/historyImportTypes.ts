import type { HistoryImportRun, HistoryImportSource } from "@/api/types";
import type { components } from "./schema";

/** Run shape used by personal history import screens, including polling state. */
export type PersonalImportRun = Omit<HistoryImportRun, "status"> & {
  status: HistoryImportRun["status"] | "canceling";
  terminal: boolean;
  cancelable: boolean;
  location?: string;
  retryAfterMs?: number;
};

export function historyImportSourceFromV2(
  source: components["schemas"]["HistoryImportSource"],
): HistoryImportSource {
  return { ...source, id: Number(source.id) };
}

/** Convert the opaque identifiers in the v2 wire response to UI identifiers. */
export function historyImportRunFromV2(
  run: components["schemas"]["HistoryImportRun"],
): PersonalImportRun {
  return {
    ...run,
    status: run.status as PersonalImportRun["status"],
    user_id: Number(run.user_id),
    mapping_id: run.mapping_id === undefined ? undefined : Number(run.mapping_id),
  };
}
