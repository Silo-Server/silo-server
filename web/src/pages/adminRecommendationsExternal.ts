import type { components } from "@/api/v2/schema";
import type { MessageKey, TextValues } from "@/i18n";
import { formatLocale } from "@/lib/appLocale";

type PlainTranslate = (id: MessageKey, values?: TextValues) => string;

type RecJobRun = components["schemas"]["AdminRecommendationJobRun"];
type RecExternalStatus = components["schemas"]["AdminRecommendationExternalStatus"];

export interface ExternalRunDetail {
  text: string;
  tone?: "warning";
}

export interface ExternalProgressBar {
  done: number;
  total: number;
  label: string;
}

const SKIP_REASON_KEYS: Readonly<Record<string, MessageKey>> = {
  no_provider: "web.admin.recommendations.external.skipNoProvider",
  no_lock: "web.admin.recommendations.external.skipNoLock",
  lock_conflict: "web.admin.recommendations.external.skipLockConflict",
  no_taste_profiles: "web.admin.recommendations.external.skipNoTasteProfiles",
};

const PHASE_KEYS: Readonly<Record<string, MessageKey>> = {
  sources: "web.admin.recommendations.external.phaseSources",
  titles: "web.admin.recommendations.external.phaseTitles",
  rows: "web.admin.recommendations.external.phaseRows",
};

/** A count for display, grouped in the format locale. */
export function countText(count: number): string {
  return count.toLocaleString(formatLocale());
}

/** A number from a run's result; 0 when the run did not report it. */
function resultNumber(value: unknown): number {
  return typeof value === "number" ? value : 0;
}

/** Live phase progress while the Discover recommendations job is running. */
export function externalProgress(
  t: PlainTranslate,
  status: RecExternalStatus,
): ExternalProgressBar | undefined {
  if (!status.running || !status.phase) return undefined;
  const total = status.progress_total ?? 0;
  if (total <= 0) return undefined;
  const key = PHASE_KEYS[status.phase];
  if (!key) return undefined;
  const done = Math.min(status.progress_done ?? 0, total);
  return {
    done,
    total,
    label: t(key, {
      done_text: countText(done),
      total_text: countText(total),
    }),
  };
}

/** What the outside-title job's last run did, a line each, under the card's counts. */
export function externalRunLines(
  t: PlainTranslate,
  run: RecJobRun | undefined,
): ExternalRunDetail[] {
  const result = run?.status === "completed" ? run.result : undefined;
  if (!result) return [];

  // Retention-only run while the feature is off: skip the misleading 0/0/0 line.
  if (result.enabled === false) {
    const pruned = resultNumber(result.pruned_titles);
    if (pruned <= 0) return [];
    return [
      {
        text: t("web.admin.recommendations.external.prunedCount", {
          count: pruned,
          count_text: countText(pruned),
        }),
      },
    ];
  }

  const embeddings = result.embeddings as Record<string, unknown> | undefined;
  const lines: ExternalRunDetail[] = [
    {
      text: t("web.admin.recommendations.external.lastRunCounts", {
        details: countText(resultNumber(result.details_fetched)),
        vectors: countText(resultNumber(embeddings?.embedded)),
        requests: countText(resultNumber(result.tmdb_requests)),
      }),
    },
  ];
  if (result.budget_exhausted === true) {
    lines.push({ text: t("web.admin.recommendations.external.budgetExhausted") });
  }
  const skipReasonKey =
    typeof result.vectors_skipped_reason === "string"
      ? SKIP_REASON_KEYS[result.vectors_skipped_reason]
      : undefined;
  if (skipReasonKey) {
    lines.push({ text: t(skipReasonKey), tone: "warning" });
  } else if (typeof result.vectors_skipped === "string" && result.vectors_skipped) {
    // Unknown reason code or older server: fall back to the server's English reason.
    lines.push({
      text: t("web.admin.recommendations.external.vectorsSkipped", {
        reason: result.vectors_skipped,
      }),
      tone: "warning",
    });
  }
  return lines;
}
