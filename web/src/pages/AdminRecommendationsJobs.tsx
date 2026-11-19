import { Loader2, Play } from "lucide-react";
import type { components } from "@/api/v2/schema";
import { Button } from "@/components/ui/button";
import { useT } from "@/i18n";
import { formatLocale } from "@/lib/appLocale";
import { formatRelativeTime } from "@/lib/date";
import { countText, externalRunLines } from "@/pages/adminRecommendationsExternal";

type RecJobRun = components["schemas"]["AdminRecommendationJobRun"];
type RecExternalStatus = components["schemas"]["AdminRecommendationExternalStatus"];

/** Which run a job card line describes: the last full run, or work between full runs. */
type RecRunKind = "run" | "catchUp" | "staleSweep" | "startupBuild";

function RecJobLastRun({ run, kind = "run" }: { run?: RecJobRun; kind?: RecRunKind }) {
  const t = useT();
  if (!run) {
    return (
      <p className="text-muted-foreground text-xs">{t("web.admin.recommendations.jobs.noRun")}</p>
    );
  }
  const when = formatRelativeTime(run.completed_at) ?? run.completed_at;
  if (run.status === "failed") {
    return (
      <div className="space-y-0.5">
        <p className="text-destructive text-xs font-medium">
          {t("web.admin.recommendations.jobs.runFailed", { kind, when })}
        </p>
        {run.error && (
          <p className="text-muted-foreground line-clamp-3 text-xs break-words" title={run.error}>
            {run.error}
          </p>
        )}
      </div>
    );
  }
  return (
    <p className="text-muted-foreground text-xs">
      {t("web.admin.recommendations.jobs.runCompleted", { kind, when })}
    </p>
  );
}

/** The outside-title job's card: titles stored, profile rows built, and its last run. */
export function RecExternalJobCard({
  status,
  pendingRestart,
  onTrigger,
  triggerPending,
}: {
  status: RecExternalStatus;
  pendingRestart: boolean;
  onTrigger: () => void;
  triggerPending: boolean;
}) {
  const t = useT();
  return (
    <RecJobStatusCard
      title={t("web.admin.recommendations.jobs.external")}
      count={status.count}
      summary={t("web.admin.recommendations.external.titlesStored", {
        count: status.count,
        count_text: countText(status.count),
      })}
      running={status.running}
      lastRun={status.last_run}
      details={[
        t("web.admin.recommendations.external.rowsBuilt", {
          count: status.rows,
          count_text: countText(status.rows),
        }),
        ...(status.enabled
          ? []
          : [
              t(
                pendingRestart
                  ? "web.admin.recommendations.external.pendingRestart"
                  : "web.admin.recommendations.external.off",
              ),
            ]),
      ]}
      runDetails={externalRunLines(t, status.last_run)}
      onTrigger={onTrigger}
      triggerPending={triggerPending}
    />
  );
}

export function RecJobStatusCard({
  title,
  count,
  summary,
  total,
  running,
  lastRun,
  secondaryRun,
  secondaryRunKind,
  note,
  details = [],
  runDetails = [],
  onTrigger,
  triggerPending,
}: {
  title: string;
  count: number;
  /** Replaces the "N entries" line. */
  summary?: string;
  total?: number;
  running: boolean;
  lastRun?: RecJobRun;
  /** Work that runs between full runs (a catch-up pass, the stale sweep), shown beside the last full run. */
  secondaryRun?: RecJobRun;
  secondaryRunKind?: RecRunKind;
  note?: string;
  /** More lines under the counts. */
  details?: string[];
  /** Lines about the last run, under it. */
  runDetails?: string[];
  onTrigger: () => void;
  triggerPending: boolean;
}) {
  const t = useT();
  const hasProgress = total !== undefined && total > 0;
  const pct = hasProgress ? Math.round((count / total) * 100) : 0;
  const isDisabled = running || triggerPending;

  return (
    <div className="surface-panel min-w-0 rounded-2xl border-0 px-4 py-4 sm:px-5">
      <div className="flex min-w-0 flex-col gap-3">
        <div className="min-w-0 space-y-1">
          <span className="text-sm font-semibold">{title}</span>
          <p className="text-muted-foreground text-xs">
            {hasProgress
              ? t("web.admin.recommendations.jobs.itemsProgress", {
                  count: total,
                  done_text: count.toLocaleString(formatLocale()),
                  count_text: total.toLocaleString(formatLocale()),
                })
              : (summary ??
                t("web.admin.recommendations.jobs.entries", {
                  count,
                  count_text: count.toLocaleString(formatLocale()),
                }))}
          </p>
          {details.map((line) => (
            <p key={line} className="text-muted-foreground text-xs">
              {line}
            </p>
          ))}
          {note && <p className="text-muted-foreground text-xs">{note}</p>}
          <RecJobLastRun run={lastRun} />
          {runDetails.map((line) => (
            <p key={line} className="text-muted-foreground text-xs break-words">
              {line}
            </p>
          ))}
          {secondaryRun && <RecJobLastRun run={secondaryRun} kind={secondaryRunKind} />}
        </div>
        <Button
          size="sm"
          variant="outline"
          onClick={onTrigger}
          disabled={isDisabled}
          className="w-full justify-center"
        >
          {running ? (
            <>
              <Loader2 className="size-3.5 animate-spin" />
              {t("web.admin.recommendations.jobs.running")}
            </>
          ) : (
            <>
              <Play className="size-3.5" />
              {t("web.admin.recommendations.jobs.run")}
            </>
          )}
        </Button>
      </div>

      {hasProgress && (
        <div className="mt-3">
          <div className="bg-muted h-2 w-full overflow-hidden rounded-full">
            <div
              className="bg-primary h-full rounded-full transition-all duration-500"
              style={{ width: `${pct}%` }}
            />
          </div>
          <p className="text-muted-foreground mt-1 text-end text-xs">
            {t("web.admin.recommendations.jobs.percent", { percent: pct })}
          </p>
        </div>
      )}
    </div>
  );
}
