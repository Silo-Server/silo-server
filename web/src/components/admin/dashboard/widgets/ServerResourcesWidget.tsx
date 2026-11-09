import { LoadMeter } from "@/components/admin/LoadMeter";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { useSystemResources } from "@/hooks/queries/admin/system";
import { usePageActivity } from "@/hooks/usePageActivity";
import { formatRelativeTime } from "@/lib/date";
import { cn } from "@/lib/utils";
import type { ResourceMetric, ResourceSamplePresentation } from "@/pages/adminNodesPresentation";
import { describeResourceSample } from "@/pages/adminNodesPresentation";
import { useT } from "@/i18n";

/** CPU, memory, and GPU usage on the API host itself. */
export function ServerResourcesWidget() {
  const t = useT();
  const pageActivity = usePageActivity();
  const resourcesQuery = useSystemResources(pageActivity.canPollDashboard);

  // A server predating the endpoint 404s. There is nothing to say about the
  // request itself, so render the same unavailable state as an unsampled host.
  const sample = describeResourceSample(resourcesQuery.isError ? undefined : resourcesQuery.data);
  const sampledLabel =
    sample.kind === "sampled"
      ? formatRelativeTime(sample.sampledAt, {
          rounding: "floor",
          justNowLabel: t("web.admin.dashboard.widgets.serverResources.justNow"),
        })
      : null;

  return (
    <Card className="h-full gap-2 py-4">
      <CardHeader className="flex shrink-0 flex-row items-center justify-between space-y-0 pb-3">
        <CardTitle className="text-sm font-bold">
          {t("web.admin.dashboard.widgets.serverResources.title")}
        </CardTitle>
        {sampledLabel ? (
          <span
            className={cn(
              "text-[11px]",
              sample.kind === "sampled" && sample.stale ? "text-warning" : "text-muted-foreground",
            )}
          >
            {t(
              sample.kind === "sampled" && sample.stale
                ? "web.admin.dashboard.widgets.serverResources.sampledStale"
                : "web.admin.dashboard.widgets.serverResources.sampled",
              { time: sampledLabel },
            )}
          </span>
        ) : null}
      </CardHeader>
      <CardContent className="min-h-0 flex-1 overflow-y-auto">
        {resourcesQuery.data === undefined && !resourcesQuery.isError ? (
          <div className="space-y-3">
            {Array.from({ length: 3 }).map((_, index) => (
              <Skeleton key={index} className="h-4 rounded" />
            ))}
          </div>
        ) : sample.kind === "unavailable" ? (
          <div className="text-muted-foreground flex h-full items-center justify-center text-center text-sm">
            {sample.title}
          </div>
        ) : (
          <SampledResources sample={sample} />
        )}
      </CardContent>
    </Card>
  );
}

function SampledResources({
  sample,
}: {
  sample: Extract<ResourceSamplePresentation, { kind: "sampled" }>;
}) {
  const t = useT();
  return (
    <div className={cn("flex min-h-full flex-col", sample.stale && "opacity-60")}>
      {/*
        One grid across every row, so the bars all start and end on the same two
        lines. The detail track is capped, so a long reading truncates instead of
        widening the card or squeezing the bars. On a taller card the rows center in the
        space above the footer.
      */}
      <div className="grid flex-1 grid-cols-[auto_minmax(2rem,1fr)_auto_fit-content(40%)] content-center items-center gap-x-3 gap-y-2.5 text-sm">
        <ResourceRow
          label={t("web.admin.dashboard.widgets.serverResources.cpu")}
          metric={sample.cpu}
        />
        <ResourceRow
          label={t("web.admin.dashboard.widgets.serverResources.memory")}
          metric={sample.memory}
        />
        {sample.gpu ? (
          <ResourceRow
            label={t("web.admin.dashboard.widgets.serverResources.gpu")}
            metric={sample.gpu}
          />
        ) : null}
      </div>
      {/* Every footer reading comes from attribution, which an older server omits. */}
      {sample.processMemory ? (
        <div className="border-border/60 text-muted-foreground mt-3 flex flex-wrap gap-x-4 gap-y-1 border-t pt-2 text-[11px]">
          <ResourceFootnote
            label={t("web.admin.dashboard.widgets.serverResources.siloProcess")}
            metric={sample.processMemory}
          />
          {sample.heapMemory ? (
            <ResourceFootnote
              label={t("web.admin.dashboard.widgets.serverResources.goHeap")}
              metric={sample.heapMemory}
            />
          ) : null}
          {sample.instanceID ? (
            <span title={sample.instanceID}>
              {t("web.admin.dashboard.widgets.serverResources.apiInstance", {
                id: sample.instanceID.slice(0, 8),
              })}
            </span>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}

/**
 * Four cells of the enclosing grid. The row's short label replaces the metric's
 * own, which moves to the tooltip: "Container host RAM" says what was measured,
 * which is worth knowing but not worth the space on every row.
 */
function ResourceRow({ label, metric }: { label: string; metric: ResourceMetric }) {
  const t = useT();
  const title = t("web.admin.dashboard.widgets.serverResources.metricTitle", {
    label: metric.label,
    title: metric.title,
  });
  return (
    <>
      <span title={title}>{label}</span>
      <LoadMeter fill={metric.fill} />
      <span
        className={cn(
          "text-right font-semibold tabular-nums",
          metric.muted && "text-muted-foreground",
        )}
        title={title}
      >
        {metric.value}
      </span>
      <span className="text-muted-foreground truncate text-xs tabular-nums" title={title}>
        {metric.detail}
      </span>
    </>
  );
}

function ResourceFootnote({ label, metric }: { label: string; metric: ResourceMetric }) {
  const t = useT();
  return (
    <span title={metric.title}>
      {t("web.admin.dashboard.widgets.serverResources.footnote", {
        label,
        value: (
          <span className={cn("font-semibold tabular-nums", !metric.muted && "text-foreground")}>
            {metric.value}
          </span>
        ),
      })}
    </span>
  );
}
