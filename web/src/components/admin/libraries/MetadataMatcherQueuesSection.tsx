import { Fragment, useState } from "react";
import { ChevronDown, Wrench } from "lucide-react";

import type { Library, LibraryMetadataMatchFailureDetail } from "@/api/types";
import { CollapsibleDiagnosticsSection } from "@/components/admin/CollapsibleDiagnosticsSection";
import {
  CARD_ACTION_CLASS,
  CardActions,
  DiagnosticsCard,
  DiagnosticsCardList,
} from "@/components/admin/libraries/DiagnosticsListParts";
import { useLibrariesTableLayout } from "@/components/admin/libraries/diagnosticsListState";
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
import { cn } from "@/lib/utils";

function formatFailureKind(kind: string): string {
  return kind.replace(/_/g, " ");
}

type MatchDecision = NonNullable<LibraryMetadataMatchFailureDetail["decision"]>;

/** One queued movie, series root, or raw file in a library's detail list. */
type MatcherDetailEntry = {
  key: string;
  path: string;
  state: string;
  failureKind?: string | null;
  message?: string | null;
  decision?: MatchDecision;
};

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
  const showTable = useLibrariesTableLayout();
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

  const detailEntries: MatcherDetailEntry[] = detail
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

  const libraryName = (libraryID: number) =>
    libraries.find((entry) => entry.id === libraryID)?.name ??
    t("web.admin.common.libraryEditor.matcher.libraryFallback", { id: libraryID });
  const toggleLibrary = (libraryID: number) => {
    setSelectedLibraryID(selectedLibraryID === libraryID ? null : libraryID);
    setDetailPageIndex(0);
  };
  const retryButton = (libraryID: number, className?: string) => (
    <Button
      size="sm"
      variant="outline"
      className={className}
      disabled={retry.isPending && retry.variables === libraryID}
      onClick={(event) => {
        event.stopPropagation();
        retry.mutate(libraryID);
      }}
    >
      {t("web.admin.common.libraryEditor.matcher.retryNow")}
    </Button>
  );
  const parkedCount = (count: number) =>
    count > 0 ? (
      <Badge variant="outline" className="border-amber-500/30 text-amber-600">
        {count}
      </Badge>
    ) : (
      0
    );
  // The selected library's entries and pager, under its table row or in its card.
  const queueDetail = (touch: boolean) => (
    <MatcherQueueDetail
      entries={detailEntries}
      loaded={detail !== undefined}
      pageIndex={detailPageIndex}
      hasPrevious={hasPreviousDetailPage}
      hasNext={hasNextDetailPage}
      fetching={detailFetching}
      onPrevious={() => setDetailPageIndex((current) => Math.max(0, current - 1))}
      onNext={() => {
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
      touch={touch}
    />
  );

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
      {showTable ? (
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
                return (
                  <Fragment key={queue.library_id}>
                    <TableRow
                      className="cursor-pointer"
                      onClick={() => toggleLibrary(queue.library_id)}
                    >
                      <TableCell className="font-medium">{libraryName(queue.library_id)}</TableCell>
                      <TableCell className="text-end tabular-nums">{queue.pending_count}</TableCell>
                      <TableCell className="text-end tabular-nums">
                        {parkedCount(queue.parked_count)}
                      </TableCell>
                      <TableCell className="text-end">{retryButton(queue.library_id)}</TableCell>
                    </TableRow>
                    {selected ? (
                      <TableRow className="hover:bg-transparent">
                        <TableCell colSpan={4} className="bg-muted/30 p-3">
                          {queueDetail(false)}
                        </TableCell>
                      </TableRow>
                    ) : null}
                  </Fragment>
                );
              })}
            </TableBody>
          </Table>
        </div>
      ) : (
        <DiagnosticsCardList>
          {queues.map((queue) => {
            const selected = selectedLibraryID === queue.library_id;
            return (
              <DiagnosticsCard key={queue.library_id}>
                {/* The card's heading opens the library's entries, as a table row click does. */}
                <button
                  type="button"
                  className="hover:bg-surface-hover/40 -m-2 flex min-h-11 w-[calc(100%+1rem)] items-center gap-3 rounded-xl p-2 text-start transition-colors"
                  aria-expanded={selected}
                  onClick={() => toggleLibrary(queue.library_id)}
                >
                  <span className="min-w-0 flex-1 space-y-1">
                    <span className="block font-medium break-words">
                      {libraryName(queue.library_id)}
                    </span>
                    <span className="text-muted-foreground flex flex-wrap items-center gap-x-4 gap-y-1 text-xs">
                      <span className="inline-flex items-center gap-1.5">
                        {t("web.admin.common.libraryEditor.matcher.pending")}
                        <span className="text-foreground tabular-nums">{queue.pending_count}</span>
                      </span>
                      <span className="inline-flex items-center gap-1.5">
                        {t("web.admin.common.libraryEditor.matcher.parked")}
                        <span className="text-foreground tabular-nums">
                          {parkedCount(queue.parked_count)}
                        </span>
                      </span>
                    </span>
                  </span>
                  <ChevronDown
                    className={cn(
                      "text-muted-foreground h-4 w-4 shrink-0 transition-transform",
                      !selected && "-rotate-90 rtl:rotate-90",
                    )}
                    aria-hidden="true"
                  />
                </button>
                {selected ? (
                  <div className="bg-muted/30 rounded-xl p-3">{queueDetail(true)}</div>
                ) : null}
                <CardActions>{retryButton(queue.library_id, CARD_ACTION_CLASS)}</CardActions>
              </DiagnosticsCard>
            );
          })}
        </DiagnosticsCardList>
      )}
    </CollapsibleDiagnosticsSection>
  );
}

/** One library's queue entries with their pager; `touch` wraps paths and enlarges controls. */
function MatcherQueueDetail({
  entries,
  loaded,
  pageIndex,
  hasPrevious,
  hasNext,
  fetching,
  onPrevious,
  onNext,
  touch,
}: {
  entries: MatcherDetailEntry[];
  loaded: boolean;
  pageIndex: number;
  hasPrevious: boolean;
  hasNext: boolean;
  fetching: boolean;
  onPrevious: () => void;
  onNext: () => void;
  touch: boolean;
}) {
  const t = useT();
  const pagerButtonClass = touch ? "h-11 flex-1" : undefined;
  return (
    <div className="space-y-2">
      {entries.map((entry) => {
        const { path, failureKind, message, decision } = entry;
        const rejection = decision ? describeRejection(decision, t) : undefined;
        const bar = decision ? scoreBar(decision) : undefined;
        return (
          <div key={entry.key} className="bg-background/70 rounded-lg border p-2 text-xs">
            <div
              className={cn(
                "flex gap-2",
                touch ? "flex-col items-start" : "items-center justify-between",
              )}
            >
              <code
                className={cn("min-w-0 font-mono", touch ? "max-w-full break-all" : "truncate")}
                dir={touch ? "ltr" : undefined}
              >
                {path}
              </code>
              <div className="flex shrink-0 flex-wrap items-center gap-1.5">
                {failureKind ? (
                  <Badge variant="outline">{formatFailureKind(failureKind)}</Badge>
                ) : null}
                <Badge variant={entry.state === "parked" ? "outline" : "secondary"}>
                  {stateLabel(entry.state, t)}
                </Badge>
              </div>
            </div>
            {rejection || message ? (
              <p className={cn("text-muted-foreground mt-1", touch && "break-words")}>
                {rejection ?? message}
              </p>
            ) : null}
            {decision?.top_candidates?.length ? (
              <div className="mt-2 space-y-1 border-t pt-2">
                {decision.top_candidates.map((candidate, candidateIndex) => (
                  <div
                    key={`${candidate.title}-${candidate.year ?? 0}-${candidate.score}-${candidateIndex}`}
                    className={cn(
                      "text-muted-foreground flex flex-wrap items-baseline gap-x-2",
                      touch && "break-words",
                    )}
                  >
                    <span className="text-foreground font-medium">{candidateTitle(candidate)}</span>
                    <span className="tabular-nums">
                      {bar === undefined
                        ? t("web.admin.common.libraryEditor.matcher.score", {
                            score: formatDecimal(candidate.score, 1),
                          })
                        : t("web.admin.common.libraryEditor.matcher.scoreOfBar", {
                            score: formatDecimal(candidate.score, 1),
                            bar: bar,
                          })}
                    </span>
                    {candidate.matched_title && candidate.matched_title !== candidate.title ? (
                      <span>
                        {t("web.admin.common.libraryEditor.matcher.matchedTitle", {
                          title: candidate.matched_title,
                        })}
                      </span>
                    ) : null}
                    {candidate.reasons?.length ? <span>{candidate.reasons.join(", ")}</span> : null}
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
      {loaded && entries.length === 0 ? (
        <p className="text-muted-foreground text-center">
          {t("web.admin.common.libraryEditor.matcher.noDetails")}
        </p>
      ) : null}
      {hasPrevious || hasNext ? (
        <div
          className={cn(
            "flex items-center justify-between pt-1",
            touch && "flex-wrap gap-x-2 gap-y-2",
          )}
        >
          <span className="text-muted-foreground text-xs">
            {t("web.admin.common.libraryEditor.matcher.page", {
              page: pageIndex + 1,
            })}
          </span>
          <div className={cn("flex gap-2", touch && "basis-full")}>
            <Button
              type="button"
              size="sm"
              variant="outline"
              className={pagerButtonClass}
              disabled={!hasPrevious || fetching}
              onClick={onPrevious}
            >
              {t("web.admin.common.libraryEditor.matcher.previous")}
            </Button>
            <Button
              type="button"
              size="sm"
              variant="outline"
              className={pagerButtonClass}
              disabled={!hasNext || fetching}
              onClick={onNext}
            >
              {t("web.admin.common.libraryEditor.matcher.next")}
            </Button>
          </div>
        </div>
      ) : null}
    </div>
  );
}
