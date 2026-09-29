import { useId, useState } from "react";
import { Loader2 } from "lucide-react";
import type { RequestMediaDetail, RequestMediaSeason } from "@/api/types";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Switch } from "@/components/ui/switch";
import { useCreateMediaRequest, useRequestMediaDetail } from "@/hooks/queries/useRequests";
import {
  defaultRequestSeasons,
  formatRequestReason,
  formatRequestSeasonMeta,
  formatSeasonList,
  requestInputFromMediaResult,
  seasonHasAired,
  seasonRequestable,
} from "@/lib/mediaRequests";
import { cn } from "@/lib/utils";

interface RequestSeasonsDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  tmdbID: number;
  /** Names the series while its detail loads. */
  title: string;
  /** The series' request detail when the caller has it; otherwise the dialog loads it when opened. */
  detail?: RequestMediaDetail;
}

/**
 * Picks the seasons of a series to request. Seasons already in the library or
 * already requested are shown but cannot be picked. Aired seasons still
 * missing start picked, and a request the viewer does not change leaves the
 * choice to the server, which picks the same ones by its own clock, or the
 * whole series when none has aired.
 */
export function RequestSeasonsDialog({
  open,
  onOpenChange,
  tmdbID,
  title,
  detail: givenDetail,
}: RequestSeasonsDialogProps) {
  const loaded = useRequestMediaDetail("series", tmdbID, { enabled: open && !givenDetail });
  const detail = givenDetail ?? loaded.data;
  // A reopened dialog can show the detail from before a request until the
  // refetch lands; don't send from it.
  const refreshing = !givenDetail && loaded.isFetching;
  const rowID = useId();
  const createRequest = useCreateMediaRequest();
  // null keeps the default pick, so it follows the detail as it loads.
  const [picked, setPicked] = useState<number[] | null>(null);

  const seasons = detail?.seasons ?? [];
  const requestable = detail?.request.requestable ?? false;
  const choices = requestable ? seasons.filter(seasonRequestable) : [];
  const defaults = defaultRequestSeasons(choices);
  const selected = (picked ?? defaults).filter((number) =>
    choices.some((season) => season.season_number === number),
  );
  const inLibrary = detail?.availability === "available";
  // Untouched, the request names no seasons and the server decides. That
  // needs something for it to pick: an aired season still missing, or a
  // series outside the library (requested whole). A series in the library
  // with only upcoming seasons left needs them picked.
  const serverChooses =
    picked === null && (seasons.length === 0 || defaults.length > 0 || !inLibrary);
  const allPicked = choices.length > 0 && choices.every((s) => selected.includes(s.season_number));

  const close = (next: boolean) => {
    if (!next) setPicked(null);
    onOpenChange(next);
  };
  const toggle = (season: number, on: boolean) =>
    setPicked(
      on ? [...selected, season].sort((a, b) => a - b) : selected.filter((s) => s !== season),
    );
  const submit = () => {
    if (!detail) return;
    createRequest.mutate(
      { ...requestInputFromMediaResult(detail), ...(!serverChooses && { seasons: selected }) },
      { onSuccess: () => close(false) },
    );
  };

  return (
    <Dialog open={open} onOpenChange={close}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Request seasons</DialogTitle>
          <DialogDescription>
            {requestable || !detail
              ? `Choose the seasons of ${detail?.title ?? title} to request.`
              : unavailableReason(detail.request.reason)}
          </DialogDescription>
        </DialogHeader>

        {!detail ? (
          <div className="text-muted-foreground flex items-center justify-center gap-2 py-10 text-sm">
            {loaded.isError ? (
              "Could not load the seasons of this series."
            ) : (
              <>
                <Loader2 className="size-4 animate-spin" aria-hidden="true" />
                Loading seasons…
              </>
            )}
          </div>
        ) : seasons.length === 0 ? (
          requestable ? (
            <p className="text-muted-foreground py-6 text-center text-sm">
              TMDB lists no seasons for this series yet; the request covers the whole series.
            </p>
          ) : null
        ) : (
          <>
            {picked === null && defaults.length === 0 && choices.length > 0 ? (
              <p className="text-muted-foreground text-sm">
                {inLibrary
                  ? "Pick the upcoming seasons to request."
                  : "No season has aired yet, so the request covers the whole series unless you pick seasons."}
              </p>
            ) : null}
            <div className="max-h-[min(60vh,28rem)] overflow-y-auto rounded-md border">
              {choices.length > 1 ? (
                <label className="bg-muted/40 flex cursor-pointer items-center gap-3 border-b px-3 py-2.5 text-sm font-medium">
                  <Switch
                    checked={allPicked}
                    onCheckedChange={(on) =>
                      setPicked(on ? choices.map((s) => s.season_number) : [])
                    }
                  />
                  All seasons
                </label>
              ) : null}
              <ul className="divide-border divide-y">
                {seasons.map((season) => {
                  const choosable = choices.includes(season);
                  const metaID = `${rowID}-${season.season_number}-meta`;
                  const statusID = `${rowID}-${season.season_number}-status`;
                  return (
                    <li key={season.season_number}>
                      <label
                        className={cn(
                          "flex items-center gap-3 px-3 py-2.5 text-sm",
                          choosable ? "cursor-pointer" : "text-muted-foreground",
                        )}
                      >
                        <Switch
                          checked={
                            choosable ? selected.includes(season.season_number) : season.requested
                          }
                          disabled={!choosable}
                          onCheckedChange={(on) => toggle(season.season_number, on)}
                          aria-label={`Season ${season.season_number}`}
                          aria-describedby={`${metaID} ${statusID}`}
                        />
                        <span className="min-w-0 flex-1">
                          <span className="block truncate font-medium">{seasonName(season)}</span>
                          <span id={metaID} className="text-muted-foreground block text-xs">
                            {formatRequestSeasonMeta(season)}
                          </span>
                        </span>
                        <SeasonStatus season={season} id={statusID} />
                      </label>
                    </li>
                  );
                })}
              </ul>
            </div>
          </>
        )}

        <DialogFooter>
          <Button variant="outline" onClick={() => close(false)}>
            Cancel
          </Button>
          <Button
            onClick={submit}
            disabled={
              !detail ||
              !requestable ||
              refreshing ||
              (!serverChooses && selected.length === 0) ||
              createRequest.isPending
            }
            className="gap-2"
          >
            {(createRequest.isPending || refreshing) && <Loader2 className="size-4 animate-spin" />}
            {selected.length > 0
              ? `Request ${formatSeasonList(selected)}`
              : serverChooses
                ? "Request series"
                : "Request"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function seasonName(season: RequestMediaSeason): string {
  const number = `Season ${season.season_number}`;
  return season.name && season.name !== number ? `${number}: ${season.name}` : number;
}

/** Why no season can be picked, for a series the viewer cannot request now. */
function unavailableReason(reason?: string): string {
  switch (reason) {
    case "already_available":
      return "Every season is already in the library.";
    case "already_requested":
      return "This series already has an open request.";
    default:
      return `${formatRequestReason(reason)}.`;
  }
}

/** Where a season stands: in the library, requested, or not out yet. */
export function SeasonStatus({
  season,
  className,
  id,
}: {
  season: RequestMediaSeason;
  className?: string;
  id?: string;
}) {
  const label =
    season.availability === "available"
      ? "In library"
      : season.requested
        ? "Requested"
        : season.availability === "partial"
          ? "Partly in library"
          : seasonHasAired(season)
            ? null
            : season.air_date && season.episode_count > 0
              ? "Not aired yet"
              : "Not announced";
  if (!label) return null;
  return (
    <span
      id={id}
      className={cn("text-muted-foreground shrink-0 text-xs whitespace-nowrap", className)}
    >
      {label}
    </span>
  );
}
