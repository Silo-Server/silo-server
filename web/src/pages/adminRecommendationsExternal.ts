import type { components } from "@/api/v2/schema";
import type { Translate } from "@/i18n";
import { formatLocale } from "@/lib/appLocale";

type RecJobRun = components["schemas"]["AdminRecommendationJobRun"];

/** A count for display, grouped in the format locale. */
export function countText(count: number): string {
  return count.toLocaleString(formatLocale());
}

/** A number from a run's result; 0 when the run did not report it. */
function resultNumber(value: unknown): number {
  return typeof value === "number" ? value : 0;
}

/** What the outside-title job's last run did, a line each, under the card's counts. */
export function externalRunLines(t: Translate, run: RecJobRun | undefined): string[] {
  const result = run?.status === "completed" ? run.result : undefined;
  if (!result) return [];
  const embeddings = result.embeddings as Record<string, unknown> | undefined;
  const lines = [
    t("web.admin.recommendations.external.lastRunCounts", {
      details: countText(resultNumber(result.details_fetched)),
      vectors: countText(resultNumber(embeddings?.embedded)),
      requests: countText(resultNumber(result.tmdb_requests)),
    }),
  ];
  if (result.budget_exhausted === true) {
    lines.push(t("web.admin.recommendations.external.budgetExhausted"));
  }
  // The server's own reason, in English, as a failed run's error is.
  if (typeof result.vectors_skipped === "string" && result.vectors_skipped) {
    lines.push(
      t("web.admin.recommendations.external.vectorsSkipped", { reason: result.vectors_skipped }),
    );
  }
  return lines;
}
