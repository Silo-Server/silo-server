import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { useSystemResources } from "@/hooks/queries/admin/system";
import { usePageActivity } from "@/hooks/usePageActivity";
import { formatRelativeTime } from "@/lib/date";
import { cn } from "@/lib/utils";
import type { ResourceMetric } from "@/pages/adminNodesPresentation";
import { describeResourceSample } from "@/pages/adminNodesPresentation";

/** CPU, memory, disk, network, and GPU usage on the API host itself. */
export function ServerResourcesWidget() {
  const pageActivity = usePageActivity();
  const resourcesQuery = useSystemResources(pageActivity.canPollDashboard);

  // A server predating the endpoint 404s. There is nothing to say about the
  // request itself, so render the same unavailable state as an unsampled host.
  const sample = describeResourceSample(resourcesQuery.isError ? undefined : resourcesQuery.data);
  const sampledLabel =
    sample.kind === "sampled"
      ? formatRelativeTime(sample.sampledAt, { rounding: "floor", justNowLabel: "just now" })
      : null;

  return (
    <Card className="h-full">
      <CardHeader className="flex shrink-0 flex-row items-center justify-between space-y-0 pb-3">
        <CardTitle className="text-sm font-bold">Server resources</CardTitle>
        {sampledLabel ? (
          <span className="text-muted-foreground text-[11px]">Sampled {sampledLabel}</span>
        ) : null}
      </CardHeader>
      <CardContent className="min-h-0 flex-1">
        {resourcesQuery.data === undefined && !resourcesQuery.isError ? (
          <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
            {Array.from({ length: 4 }).map((_, index) => (
              <Skeleton key={index} className="h-[76px] rounded-lg" />
            ))}
          </div>
        ) : sample.kind === "unavailable" ? (
          <div className="text-muted-foreground flex h-full items-center justify-center text-center text-sm">
            {sample.title}
          </div>
        ) : (
          <div className="grid gap-3 text-sm sm:grid-cols-2 lg:grid-cols-4">
            <ResourceMetricBox metric={sample.cpu} />
            <ResourceMetricBox metric={sample.memory} />
            <ResourceMetricBox metric={sample.disk} />
            <ResourceMetricBox metric={sample.gpu ?? sample.network} />
          </div>
        )}
      </CardContent>
    </Card>
  );
}

function ResourceMetricBox({ metric }: { metric: ResourceMetric }) {
  return (
    <div className="border-border/60 rounded-lg border p-3" title={metric.title}>
      <div className="text-muted-foreground text-xs">{metric.label}</div>
      <div
        className={cn(
          "mt-1 text-xl font-bold tracking-tight tabular-nums",
          metric.muted && "text-muted-foreground",
          metric.warning && "text-warning",
        )}
      >
        {metric.value}
      </div>
      <div className="text-muted-foreground mt-0.5 text-xs">{metric.detail}</div>
    </div>
  );
}
