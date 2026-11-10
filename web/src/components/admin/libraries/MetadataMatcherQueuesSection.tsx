import { Fragment, useState } from "react";
import { Wrench } from "lucide-react";

import type { Library, LibraryMetadataMatchFailureDetail } from "@/api/types";
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
import { useT, type Translate } from "@/i18n";
import { formatDecimal } from "@/lib/numberFormat";

function formatFailureKind(kind: string): string {
  return kind.replace(/_/g, " ");
}

type MatchDecision = NonNullable<LibraryMetadataMatchFailureDetail["decision"]>;

/** Why the matcher turned the candidates down, for a candidate_rejected decision. */
function describeRejection(decision: MatchDecision, t: Translate): string | undefined {
  if (decision.outcome !== "candidate_rejected") return undefined;
  switch (decision.rejection) {
    case "below_floor":
      return t("web.admin.common.libraryEditor.matcher.belowFloor");
    case "lone_candidate":
      return t("web.admin.common.libraryEditor.matcher.loneCandidate");
    case "year_mismatch":
      return t("web.admin.common.libraryEditor.matcher.yearMismatch");
    case "ambiguous":
      return t("web.admin.common.libraryEditor.matcher.ambiguous");
    case "trusted_id_missing":
      return t("web.admin.common.libraryEditor.matcher.trustedIdMissing");
    default:
      return undefined;
  }
}

/** A candidate's title with its year, as the provider gave them. */
function candidateTitle(candidate: { title: string; year?: number | null }): string {
  return candidate.year ? `${candidate.title} (${candidate.year})` : candidate.title;
}

/** A queue entry's state badge; a state this build does not know shows as sent. */
function stateLabel(state: string, t: Translate): string {
  if (state === "pending") return t("web.admin.common.libraryEditor.matcher.statePending");
  if (state === "parked") return t("web.admin.common.libraryEditor.matcher.stateParked");
  return state;
}

/** The score a candidate had to reach, or undefined when the rule is not a score bar. */
function scoreBar(decision: MatchDecision): number | undefined {
  if (decision.required_score) return decision.required_score;
  // A year mismatch or a tie has no score bar. Decisions recorded before the
  // rule was named fall back to the acceptance floor.
  return decision.rejection ? undefined : decision.threshold;
}

export function MetadataMatcherQueuesSection({ libraries }: { libraries: Library[] }) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const [selectedLibraryID, setSelectedLibraryID] = useState<number | null>(null);
  // Index into the loaded pages of the detail query; the query retains every
  // page's cursor so the periodic refetch refreshes them in place.
  const [detailPageIndex, setDetailPageIndex] = useState(0);
  const { data: queues = [] } = useLibraryMetadataMatchQueues();
  // Passing null while collapsed disables the detail query entirely so a
  // hidden expansion does not keep polling the per-library endpoint.
  const {
    data: detailPages,
    isFetching: detailFetching,
    hasNextPage,
    fetchNextPage,
  } = useLibraryMetadataMatchQueueDetail(open ? selectedLibraryID : null);
  const retry = useRetryLibraryMetadataMatchQueue();
  const total = queues.reduce((sum, queue) => sum + queue.total_count, 0);
  const loadedPages = detailPages?.pages ?? [];
  const detail = loadedPages[Math.min(detailPageIndex, Math.max(loadedPages.length - 1, 0))];
  const hasPreviousDetailPage = detailPageIndex > 0;
  const hasNextDetailPage = detailPageIndex + 1 < loadedPages.length || hasNextPage;

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
          key: `series-${entry.library_id}-${entry.observed_root_path}`,
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
            message: identity
              ? t("web.admin.common.libraryEditor.matcher.awaitingMatchFor", { identity })
              : t("web.admin.common.libraryEditor.matcher.awaitingMatch"),
            decision: undefined,
          };
        }),
      ]
    : [];

  if (total === 0) return null;

  return (
    <CollapsibleDiagnosticsSection
      title={t("web.admin.common.libraryEditor.matcher.title")}
      description={t("web.admin.common.libraryEditor.matcher.description")}
      count={total}
      icon={<Wrench className="h-4 w-4 text-amber-500" />}
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (!next) {
          setSelectedLibraryID(null);
          setDetailPageIndex(0);
        }
      }}
    >
      <div className="overflow-hidden rounded-xl border">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t("web.admin.common.libraryEditor.matcher.library")}</TableHead>
              <TableHead className="text-end">
                {t("web.admin.common.libraryEditor.matcher.pending")}
              </TableHead>
              <TableHead className="text-end">
                {t("web.admin.common.libraryEditor.matcher.parked")}
              </TableHead>
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
                      setDetailPageIndex(0);
                    }}
                  >
                    <TableCell className="font-medium">
                      {library?.name ??
                        t("web.admin.common.libraryEditor.matcher.libraryFallback", {
                          id: String(queue.library_id),
                        })}
                    </TableCell>
                    <TableCell className="text-end tabular-nums">{queue.pending_count}</TableCell>
                    <TableCell className="text-end tabular-nums">
                      {queue.parked_count > 0 ? (
                        <Badge variant="outline" className="border-amber-500/30 text-amber-600">
                          {queue.parked_count}
                        </Badge>
                      ) : (
                        0
                      )}
                    </TableCell>
                    <TableCell className="text-end">
                      <Button
                        size="sm"
                        variant="outline"
                        disabled={retry.isPending && retry.variables === queue.library_id}
                        onClick={(event) => {
                          event.stopPropagation();
                          retry.mutate(queue.library_id);
                        }}
                      >
                        {t("web.admin.common.libraryEditor.matcher.retryNow")}
                      </Button>
                    </TableCell>
                  </TableRow>
                  {selected ? (
                    <TableRow className="hover:bg-transparent">
                      <TableCell colSpan={4} className="bg-muted/30 p-3">
                        <div className="space-y-2">
                          {detailEntries.map((entry) => {
                            const { path, failureKind, message, decision } = entry;
                            const rejection = decision ? describeRejection(decision, t) : undefined;
                            const bar = decision ? scoreBar(decision) : undefined;
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
                                      {stateLabel(entry.state, t)}
                                    </Badge>
                                  </div>
                                </div>
                                {rejection || message ? (
                                  <p className="text-muted-foreground mt-1">
                                    {rejection ?? message}
                                  </p>
                                ) : null}
                                {decision?.top_candidates?.length ? (
                                  <div className="mt-2 space-y-1 border-t pt-2">
                                    {decision.top_candidates.map((candidate, candidateIndex) => (
                                      <div
                                        key={`${candidate.title}-${candidate.year ?? 0}-${candidate.score}-${candidateIndex}`}
                                        className="text-muted-foreground flex flex-wrap items-baseline gap-x-2"
                                      >
                                        <span className="text-foreground font-medium">
                                          {candidateTitle(candidate)}
                                        </span>
                                        <span className="tabular-nums">
                                          {bar === undefined
                                            ? t("web.admin.common.libraryEditor.matcher.score", {
                                                score: formatDecimal(candidate.score, 1),
                                              })
                                            : t(
                                                "web.admin.common.libraryEditor.matcher.scoreOfBar",
                                                {
                                                  score: formatDecimal(candidate.score, 1),
                                                  bar: String(bar),
                                                },
                                              )}
                                        </span>
                                        {candidate.matched_title &&
                                        candidate.matched_title !== candidate.title ? (
                                          <span>
                                            {t(
                                              "web.admin.common.libraryEditor.matcher.matchedTitle",
                                              {
                                                title: candidate.matched_title,
                                              },
                                            )}
                                          </span>
                                        ) : null}
                                        {candidate.reasons?.length ? (
                                          <span>{candidate.reasons.join(", ")}</span>
                                        ) : null}
                                        {candidate.sources?.length ? (
                                          <span>
                                            {t("web.admin.common.libraryEditor.matcher.via", {
                                              sources: candidate.sources.join(", "),
                                            })}
                                          </span>
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
                              {t("web.admin.common.libraryEditor.matcher.noDetails")}
                            </p>
                          ) : null}
                          {hasPreviousDetailPage || hasNextDetailPage ? (
                            <div className="flex items-center justify-between pt-1">
                              <span className="text-muted-foreground text-xs">
                                {t("web.admin.common.libraryEditor.matcher.page", {
                                  page: String(detailPageIndex + 1),
                                })}
                              </span>
                              <div className="flex gap-2">
                                <Button
                                  type="button"
                                  size="sm"
                                  variant="outline"
                                  disabled={!hasPreviousDetailPage || detailFetching}
                                  onClick={() =>
                                    setDetailPageIndex((current) => Math.max(0, current - 1))
                                  }
                                >
                                  {t("web.admin.common.libraryEditor.matcher.previous")}
                                </Button>
                                <Button
                                  type="button"
                                  size="sm"
                                  variant="outline"
                                  disabled={!hasNextDetailPage || detailFetching}
                                  onClick={() => {
                                    if (detailPageIndex + 1 < loadedPages.length) {
                                      setDetailPageIndex((current) => current + 1);
                                      return;
                                    }
                                    // The next page is not loaded yet: fetch it, then show it.
                                    void fetchNextPage().then((result) => {
                                      if ((result.data?.pages.length ?? 0) > detailPageIndex + 1) {
                                        setDetailPageIndex(detailPageIndex + 1);
                                      }
                                    });
                                  }}
                                >
                                  {t("web.admin.common.libraryEditor.matcher.next")}
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
