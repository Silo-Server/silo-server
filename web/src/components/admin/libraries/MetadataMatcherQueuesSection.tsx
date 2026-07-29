import { Fragment, useState } from "react";
import { Wrench } from "lucide-react";

import type { Library } from "@/api/types";
import { CollapsibleDiagnosticsSection } from "@/components/admin/CollapsibleDiagnosticsSection";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  useLibraryMetadataMatchQueueDetail,
  useLibraryMetadataMatchQueues,
  useRetryLibraryMetadataMatchQueue,
} from "@/hooks/queries/admin/libraries";

function formatFailureKind(kind: string): string {
  return kind.replace(/_/g, " ");
}

export function MetadataMatcherQueuesSection({ libraries }: { libraries: Library[] }) {
  const [open, setOpen] = useState(false);
  const [selectedLibraryID, setSelectedLibraryID] = useState<number | null>(null);
  const [detailOffset, setDetailOffset] = useState(0);
  const { data: queues = [] } = useLibraryMetadataMatchQueues();
  // Passing null while collapsed disables the detail query entirely so a
  // hidden expansion does not keep polling the per-library endpoint.
  const { data: detail, isFetching: detailFetching } = useLibraryMetadataMatchQueueDetail(
    open ? selectedLibraryID : null,
    detailOffset,
  );
  const retry = useRetryLibraryMetadataMatchQueue();
  const total = queues.reduce((sum, queue) => sum + queue.total_count, 0);
  const detailLimit = detail?.limit ?? 10;
  const hasPreviousDetailPage = detailOffset > 0;
  const hasNextDetailPage = detail
    ? detailOffset + detail.movies.length < detail.movie_count ||
      detailOffset + detail.series.length < detail.series_count ||
      detailOffset + detail.raw_files.length < detail.raw_file_count
    : false;

  const detailEntries = detail
    ? [
        ...detail.movies.map((entry) => ({
          key: `movie-${entry.media_file_id}`,
          path: entry.file_path,
          state: entry.state,
          failureKind: entry.failure_kind,
          message: entry.failure_detail?.message ?? entry.last_error,
          decision: entry.failure_detail?.decision,
        })),
        ...detail.series.map((entry) => ({
          key: `series-${entry.media_folder_id}-${entry.observed_root_path}`,
          path: entry.observed_root_path,
          state: entry.state,
          failureKind: entry.failure_kind,
          message: entry.failure_detail?.message ?? entry.last_error,
          decision: entry.failure_detail?.decision,
        })),
        ...detail.raw_files.map((entry) => {
          const identity = [
            entry.base_title,
            entry.base_year ? `(${entry.base_year})` : "",
            entry.base_type ? `[${entry.base_type}]` : "",
          ]
            .filter(Boolean)
            .join(" ");
          return {
            key: `raw-${entry.media_file_id}`,
            path: entry.file_path,
            state: "pending" as const,
            failureKind: null,
            message: identity ? `Awaiting initial match: ${identity}` : "Awaiting initial match",
            decision: undefined,
          };
        }),
      ]
    : [];

  if (total === 0) return null;

  return (
    <CollapsibleDiagnosticsSection
      title="Metadata Matcher"
      description="Pending and parked items that still need a provider match."
      count={total}
      icon={<Wrench className="h-4 w-4 text-amber-500" />}
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (!next) {
          setSelectedLibraryID(null);
          setDetailOffset(0);
        }
      }}
    >
      <div className="overflow-hidden rounded-xl border">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Library</TableHead>
              <TableHead className="text-right">Pending</TableHead>
              <TableHead className="text-right">Parked</TableHead>
              <TableHead className="w-28" />
            </TableRow>
          </TableHeader>
          <TableBody>
            {queues.map((queue) => {
              const selected = selectedLibraryID === queue.library_id;
              const library = libraries.find((entry) => entry.id === queue.library_id);
              return (
                <Fragment key={queue.library_id}>
                  <TableRow
                    className="cursor-pointer"
                    onClick={() => {
                      setSelectedLibraryID(selected ? null : queue.library_id);
                      setDetailOffset(0);
                    }}
                  >
                    <TableCell className="font-medium">
                      {library?.name ?? `Library ${queue.library_id}`}
                    </TableCell>
                    <TableCell className="text-right tabular-nums">{queue.pending_count}</TableCell>
                    <TableCell className="text-right tabular-nums">
                      {queue.parked_count > 0 ? (
                        <Badge variant="outline" className="border-amber-500/30 text-amber-600">
                          {queue.parked_count}
                        </Badge>
                      ) : (
                        0
                      )}
                    </TableCell>
                    <TableCell className="text-right">
                      <Button
                        size="sm"
                        variant="outline"
                        disabled={retry.isPending && retry.variables === queue.library_id}
                        onClick={(event) => {
                          event.stopPropagation();
                          retry.mutate(queue.library_id);
                        }}
                      >
                        Retry now
                      </Button>
                    </TableCell>
                  </TableRow>
                  {selected ? (
                    <TableRow className="hover:bg-transparent">
                      <TableCell colSpan={4} className="bg-muted/30 p-3">
                        <div className="space-y-2">
                          {detailEntries.map((entry) => {
                            const { path, failureKind, message, decision } = entry;
                            return (
                              <div
                                key={entry.key}
                                className="bg-background/70 rounded-lg border p-2 text-xs"
                              >
                                <div className="flex items-center justify-between gap-2">
                                  <code className="min-w-0 truncate font-mono">{path}</code>
                                  <div className="flex shrink-0 items-center gap-1.5">
                                    {failureKind ? (
                                      <Badge variant="outline">
                                        {formatFailureKind(failureKind)}
                                      </Badge>
                                    ) : null}
                                    <Badge
                                      variant={entry.state === "parked" ? "outline" : "secondary"}
                                    >
                                      {entry.state}
                                    </Badge>
                                  </div>
                                </div>
                                {message ? (
                                  <p className="text-muted-foreground mt-1">{message}</p>
                                ) : null}
                                {decision?.top_candidates?.length ? (
                                  <div className="mt-2 space-y-1 border-t pt-2">
                                    {decision.top_candidates.map((candidate, candidateIndex) => (
                                      <div
                                        key={`${candidate.title}-${candidate.year ?? 0}-${candidate.score}-${candidateIndex}`}
                                        className="text-muted-foreground flex flex-wrap items-baseline gap-x-2"
                                      >
                                        <span className="text-foreground font-medium">
                                          {candidate.title}
                                          {candidate.year ? ` (${candidate.year})` : ""}
                                        </span>
                                        <span className="tabular-nums">
                                          score {candidate.score.toFixed(1)} / {decision.threshold}
                                        </span>
                                        {candidate.matched_title &&
                                        candidate.matched_title !== candidate.title ? (
                                          <span>matched “{candidate.matched_title}”</span>
                                        ) : null}
                                        {candidate.reasons?.length ? (
                                          <span>{candidate.reasons.join(", ")}</span>
                                        ) : null}
                                        {candidate.sources?.length ? (
                                          <span>via {candidate.sources.join(", ")}</span>
                                        ) : null}
                                      </div>
                                    ))}
                                  </div>
                                ) : null}
                              </div>
                            );
                          })}
                          {detail && detailEntries.length === 0 ? (
                            <p className="text-muted-foreground text-center">
                              No queued item details.
                            </p>
                          ) : null}
                          {hasPreviousDetailPage || hasNextDetailPage ? (
                            <div className="flex items-center justify-between pt-1">
                              <span className="text-muted-foreground text-xs">
                                Page {Math.floor(detailOffset / detailLimit) + 1}
                              </span>
                              <div className="flex gap-2">
                                <Button
                                  type="button"
                                  size="sm"
                                  variant="outline"
                                  disabled={!hasPreviousDetailPage || detailFetching}
                                  onClick={() =>
                                    setDetailOffset((current) => Math.max(0, current - detailLimit))
                                  }
                                >
                                  Previous
                                </Button>
                                <Button
                                  type="button"
                                  size="sm"
                                  variant="outline"
                                  disabled={!hasNextDetailPage || detailFetching}
                                  onClick={() =>
                                    setDetailOffset((current) => current + detailLimit)
                                  }
                                >
                                  Next
                                </Button>
                              </div>
                            </div>
                          ) : null}
                        </div>
                      </TableCell>
                    </TableRow>
                  ) : null}
                </Fragment>
              );
            })}
          </TableBody>
        </Table>
      </div>
    </CollapsibleDiagnosticsSection>
  );
}
