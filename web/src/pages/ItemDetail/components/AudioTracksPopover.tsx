import { Check, ChevronDown, Volume2 } from "lucide-react";
import type { ReactNode } from "react";

import type { FileVersion } from "@/api/types";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { getLanguageName } from "@/player/utils/languageNames";
import { formatChannels, mapAudioLabel } from "@/lib/mediaFormat";
import { audioTitle, compactAudioMeta } from "./versionFormatUtils";
import { formatAudioTrackSummary, resolveAudioTrackSelection } from "./prePlaySelection";

interface AudioTracksPopoverProps {
  version: FileVersion | null;
  selectionMode?: "auto" | "explicit";
  explicitTrackIndex?: number | null;
  onSelectTrack?: (trackIndex: number) => void;
  onResetSelection?: () => void;
}

function AudioOptionRow({
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
        <div className="flex flex-wrap items-center gap-2">
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
    return (
      <div
        className={`flex items-start gap-3 rounded-lg px-3 py-2.5 ${active ? "bg-accent/50" : ""}`}
      >
        {content}
      </div>
    );
  }

  return (
    <button
      type="button"
      onClick={onSelect}
      className={`flex w-full items-start gap-3 rounded-lg px-3 py-2.5 text-left transition-colors ${
        active ? "bg-accent/50" : "hover:bg-accent/30"
      }`}
    >
      {content}
    </button>
  );
}

export default function AudioTracksPopover({
  version,
  selectionMode = "auto",
  explicitTrackIndex = null,
  onSelectTrack,
  onResetSelection,
}: AudioTracksPopoverProps) {
  const tracks = version?.audio_tracks;
  if (!tracks || tracks.length === 0) return null;

  const isInteractive = Boolean(onSelectTrack || onResetSelection);

  const { autoTrack, activeTrack } = resolveAudioTrackSelection(
    version,
    selectionMode === "explicit" ? explicitTrackIndex : null,
  );
  const activeSummary = formatAudioTrackSummary(activeTrack);
  const autoSummary = formatAudioTrackSummary(autoTrack);

  return (
    <Popover>
      <PopoverTrigger asChild>
        <Button
          variant="glass"
          className="h-8 max-w-full min-w-0 shrink gap-1.5 rounded-full px-3 text-xs font-medium"
        >
          <Volume2 className="size-3.5" />
          Audio
          <span className="text-muted-foreground max-w-24 truncate text-[11px] font-normal sm:max-w-28">
            {isInteractive
              ? selectionMode === "auto"
                ? `Auto: ${autoSummary}`
                : activeSummary
              : tracks.length}
          </span>
          <ChevronDown className="text-muted-foreground size-3" />
        </Button>
      </PopoverTrigger>
      <PopoverContent className="w-80 p-1.5">
        <div className="space-y-0.5">
          {isInteractive && (
            <AudioOptionRow
              active={selectionMode === "auto"}
              title="Auto"
              description={autoSummary}
              onSelect={onResetSelection}
            />
          )}
          {tracks.map((track, index) => {
            const codec = track.codec ? mapAudioLabel(track.codec) : "";
            const channels = formatChannels(track.channels);
            const title = audioTitle(track);
            const language = getLanguageName(track.language ?? "");
            const meta = [language && language !== title ? language : "", compactAudioMeta(track)]
              .filter(Boolean)
              .join(" \u00B7 ");

            return (
              <AudioOptionRow
                key={`${track.language}-${track.codec}-${index}`}
                active={
                  isInteractive
                    ? selectionMode === "explicit" && explicitTrackIndex === index
                    : Boolean(track.default)
                }
                title={title}
                description={meta}
                onSelect={onSelectTrack ? () => onSelectTrack(index) : undefined}
                badges={
                  <>
                    {codec && (
                      <Badge variant="secondary" className="px-1.5 py-0 text-[10px] uppercase">
                        {codec}
                      </Badge>
                    )}
                    {channels && (
                      <Badge variant="secondary" className="px-1.5 py-0 text-[10px]">
                        {channels}
                      </Badge>
                    )}
                    {track.default && (
                      <Badge variant="outline" className="px-1.5 py-0 text-[10px] uppercase">
                        Default
                      </Badge>
                    )}
                  </>
                }
              />
            );
          })}
        </div>
      </PopoverContent>
    </Popover>
  );
}
