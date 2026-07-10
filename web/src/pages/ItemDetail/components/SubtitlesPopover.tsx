import { useMemo, useState } from "react";
import type { ReactNode } from "react";
import { Captions, Check, ChevronDown, Loader2 } from "lucide-react";

import type { FileVersion } from "@/api/types";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { useDownloadedSubtitles } from "@/hooks/queries/subtitles";
import type {
  PlayerSubtitleTrackSignature,
  PrePlaySubtitleSelection,
  SubtitleMode,
} from "@/player/types";
import { getLanguageName } from "@/player/utils/languageNames";
import { getSubtitleFormatLabel, isSubtitleFormatLabel } from "@/player/utils/subtitleCodecs";
import {
  buildPrePlaySubtitleCandidates,
  formatSubtitlePillSummary,
  inferSubtitleFlagsFromTitle,
  resolveSelectedAudioLanguage,
  resolveAutoSubtitleSelection,
  subtitleSelectionEquals,
  type PrePlaySubtitleCandidate,
} from "./prePlaySelection";

interface SubtitlesPopoverProps {
  version: FileVersion | null;
  selectionMode?: "auto" | "off" | "explicit";
  explicitSelection?: PrePlaySubtitleSelection | null;
  preferredSubtitleLanguage?: string | null;
  preferredSubtitleTrackSignature?: PlayerSubtitleTrackSignature | null;
  subtitleMode?: SubtitleMode;
  showForcedSubtitles?: boolean;
  profileLanguage?: string | null;
  activeAudioTrackIndex?: number | null;
  onSelectSubtitle?: (selection: PrePlaySubtitleSelection) => void;
  onSelectSubtitleOff?: () => void;
  onResetSelection?: () => void;
}

function SubtitleFlagBadges({
  forced,
  hearingImpaired,
  isDefault,
}: {
  forced?: boolean;
  hearingImpaired?: boolean;
  isDefault?: boolean;
}) {
  return (
    <>
      {forced && (
        <Badge variant="outline" className="px-1.5 py-0 text-[10px] uppercase">
          Forced
        </Badge>
      )}
      {hearingImpaired && (
        <Badge variant="outline" className="px-1.5 py-0 text-[10px] uppercase">
          HI
        </Badge>
      )}
      {isDefault && (
        <Badge variant="outline" className="px-1.5 py-0 text-[10px] uppercase">
          Default
        </Badge>
      )}
    </>
  );
}

function SelectionRow({
  active,
  title,
  description,
  badges,
  onSelect,
}: {
  active: boolean;
  title: string;
  description?: string;
  badges?: ReactNode;
  onSelect?: () => void;
}) {
  const content = (
    <>
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-x-1.5 gap-y-1">
          <span className="text-sm font-medium">{title}</span>
          {badges}
        </div>
        {description && <div className="text-muted-foreground mt-0.5 text-xs">{description}</div>}
      </div>
      <div className="flex w-4 shrink-0 justify-end">
        {active && <Check className="text-primary size-4" />}
      </div>
    </>
  );

  if (!onSelect) {
    return <div className="flex items-start gap-3 rounded-lg px-3 py-2">{content}</div>;
  }

  return (
    <button
      type="button"
      onClick={onSelect}
      className={`flex w-full items-start gap-3 rounded-lg px-3 py-2 text-left transition-colors ${
        active ? "bg-accent/50" : "hover:bg-accent/30"
      }`}
    >
      {content}
    </button>
  );
}

function SubtitleSection({
  title,
  rows,
  activeSelection,
  onSelectSubtitle,
}: {
  title: string;
  rows: ReturnType<typeof buildPrePlaySubtitleCandidates>["embedded"];
  activeSelection: PrePlaySubtitleSelection | null;
  onSelectSubtitle?: (selection: PrePlaySubtitleSelection) => void;
}) {
  if (rows.length === 0) return null;

  return (
    <div>
      <div className="text-muted-foreground/60 mb-1 px-3 text-[11px] font-medium">{title}</div>
      <div className="space-y-0.5">
        {rows.map((row) => {
          const titleFlags = inferSubtitleFlagsFromTitle(row.title);
          const description =
            row.title && !titleFlags.flagOnly && !isSubtitleFormatLabel(row.title, row.codec)
              ? row.title
              : row.releaseName;

          return (
            <SelectionRow
              key={row.key}
              active={subtitleSelectionEquals(activeSelection, row.selection)}
              title={row.summary}
              description={description}
              onSelect={onSelectSubtitle ? () => onSelectSubtitle(row.selection) : undefined}
              badges={
                <>
                  {row.codec && (
                    <Badge variant="secondary" className="px-1.5 py-0 text-[10px] uppercase">
                      {getSubtitleFormatLabel(row.codec) || row.codec.toUpperCase()}
                    </Badge>
                  )}
                  <SubtitleFlagBadges
                    forced={row.forced || titleFlags.forced}
                    hearingImpaired={row.hearingImpaired || titleFlags.hearingImpaired}
                    isDefault={row.default}
                  />
                </>
              }
            />
          );
        })}
      </div>
    </div>
  );
}

function formatExplicitSelectionSummary(
  selection: PrePlaySubtitleSelection | null | undefined,
): string {
  if (!selection) return "Off";
  return formatSubtitlePillSummary({
    label: selection.label,
    languageLabel: getLanguageName(selection.language ?? "") || selection.language || "Unknown",
    codec: selection.codec,
    forced: selection.forced,
    hearingImpaired: selection.hearing_impaired,
  });
}

function formatCandidatePillSummary(candidate: PrePlaySubtitleCandidate): string {
  return formatSubtitlePillSummary({
    label: candidate.selection.label,
    languageLabel: candidate.languageLabel,
    codec: candidate.codec,
    forced: candidate.forced,
    hearingImpaired: candidate.hearingImpaired,
  });
}

export default function SubtitlesPopover({
  version,
  selectionMode = "auto",
  explicitSelection = null,
  preferredSubtitleLanguage,
  preferredSubtitleTrackSignature,
  subtitleMode,
  showForcedSubtitles = true,
  profileLanguage,
  activeAudioTrackIndex = null,
  onSelectSubtitle,
  onSelectSubtitleOff,
  onResetSelection,
}: SubtitlesPopoverProps) {
  const [open, setOpen] = useState(false);
  // Downloaded subtitles normally load lazily on open, but when the saved
  // preference points at one, the closed trigger's Auto summary needs them
  // to reflect the override.
  const needsDownloaded = open || preferredSubtitleTrackSignature?.source === "downloaded";
  const downloadedQuery = useDownloadedSubtitles(needsDownloaded ? version?.file_id : undefined);
  const isInteractive = Boolean(onSelectSubtitle || onSelectSubtitleOff || onResetSelection);

  const candidates = useMemo(
    () => buildPrePlaySubtitleCandidates(version?.subtitle_tracks, downloadedQuery.data ?? []),
    [downloadedQuery.data, version?.subtitle_tracks],
  );
  const activeAudioLanguage = useMemo(
    () => resolveSelectedAudioLanguage(version, activeAudioTrackIndex),
    [activeAudioTrackIndex, version],
  );
  const autoCandidate = useMemo(
    () =>
      resolveAutoSubtitleSelection({
        candidates: candidates.all,
        preferredSubtitleLanguage,
        preferredSubtitleTrackSignature,
        subtitleMode,
        showForcedSubtitles,
        audioLanguage: activeAudioLanguage,
        profileLanguage,
      }),
    [
      activeAudioLanguage,
      candidates.all,
      preferredSubtitleLanguage,
      preferredSubtitleTrackSignature,
      profileLanguage,
      showForcedSubtitles,
      subtitleMode,
    ],
  );

  if (!version) return null;

  // A stored track signature means a manual override is saved for this item;
  // present the resolved track as the selection rather than an "Auto" guess.
  const overrideCandidate =
    selectionMode === "auto" && preferredSubtitleTrackSignature ? autoCandidate : null;

  const activeSummary =
    selectionMode === "auto"
      ? overrideCandidate
        ? formatCandidatePillSummary(overrideCandidate)
        : `Auto: ${autoCandidate ? formatCandidatePillSummary(autoCandidate) : "Off"}`
      : selectionMode === "off"
        ? "Off"
        : formatExplicitSelectionSummary(explicitSelection);

  const activeListSelection =
    !isInteractive || selectionMode === "off"
      ? null
      : selectionMode === "explicit"
        ? explicitSelection
        : (overrideCandidate?.selection ?? null);

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button
          variant="glass"
          className="h-8 max-w-full min-w-0 shrink gap-1.5 rounded-full px-3 text-xs font-medium"
        >
          <Captions className="size-3.5" />
          Subs
          <span className="text-muted-foreground max-w-44 truncate text-[11px] font-normal sm:max-w-64">
            {isInteractive ? activeSummary : candidates.all.length}
          </span>
          <ChevronDown className="text-muted-foreground size-3" />
        </Button>
      </PopoverTrigger>
      <PopoverContent className="max-h-80 w-80 overflow-y-auto p-1.5">
        <div className="space-y-2 py-1">
          {isInteractive && (
            <>
              <SelectionRow
                active={selectionMode === "auto" && !overrideCandidate}
                title="Auto"
                description={
                  overrideCandidate
                    ? "Reset to profile defaults"
                    : (autoCandidate?.summary ?? "Off")
                }
                onSelect={onResetSelection}
              />
              <SelectionRow
                active={selectionMode === "off"}
                title="Off"
                onSelect={onSelectSubtitleOff}
              />
            </>
          )}
          <SubtitleSection
            title="Embedded"
            rows={candidates.embedded}
            activeSelection={activeListSelection}
            onSelectSubtitle={onSelectSubtitle}
          />
          <SubtitleSection
            title="External"
            rows={candidates.external}
            activeSelection={activeListSelection}
            onSelectSubtitle={onSelectSubtitle}
          />
          {downloadedQuery.isLoading ? (
            <div className="text-muted-foreground flex items-center gap-2 px-3 py-2 text-xs">
              <Loader2 className="size-3 animate-spin" />
              Loading downloaded...
            </div>
          ) : (
            <SubtitleSection
              title="Downloaded"
              rows={candidates.downloaded}
              activeSelection={activeListSelection}
              onSelectSubtitle={onSelectSubtitle}
            />
          )}
          {candidates.all.length === 0 && !downloadedQuery.isLoading && (
            <div className="text-muted-foreground px-3 py-2 text-sm">No subtitles available.</div>
          )}
        </div>
      </PopoverContent>
    </Popover>
  );
}
