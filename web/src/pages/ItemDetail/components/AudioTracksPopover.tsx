import { Check, ChevronDown, Loader2, Volume2 } from "lucide-react";
import { memo, useCallback, type ReactNode } from "react";

import type { FileVersion } from "@/api/types";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { getLanguageName } from "@/player/utils/languageNames";
import { audioTrackLabel, formatChannels } from "@/lib/mediaFormat";
import { audioTitle, compactAudioMeta } from "./versionFormatUtils";
import { formatAudioTrackSummary, resolveAudioTrackSelection } from "./prePlaySelection";
import { useProvisionalInventoryExpiry } from "@/player/hooks/useProvisionalInventoryExpiry";
import DetailPopover from "./DetailPopover";

interface AudioTracksPopoverProps {
  version: FileVersion | null;
  selectionMode?: "auto" | "explicit";
  explicitTrackIndex?: number | null;
  onSelectTrack?: (trackIndex: number) => void;
  onResetSelection?: () => void;
  /**
   * True while the selected row has no audio rows at all and no probe evidence
   * has landed. The menu says it is still discovering instead of inventing a
   * list; a row that carries declared tracks never reaches this state.
   */
  inventoryProvisional?: boolean;
  /**
   * True while the rows are the selected row's release listing rather than
   * probe evidence. They are shown immediately and labelled as unverified.
   */
  inventoryDeclared?: boolean;
  /** Re-runs inventory discovery; shown once the provisional window lapses. */
  onRetryInventory?: () => void;
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

function AudioTracksPopover({
  version,
  selectionMode = "auto",
  explicitTrackIndex = null,
  onSelectTrack,
  onResetSelection,
  inventoryProvisional = false,
  inventoryDeclared = false,
  onRetryInventory,
}: AudioTracksPopoverProps) {
  const tracks = version?.audio_tracks ?? [];
  const { expired: discoveryExpired, retry: restartDiscovery } =
    useProvisionalInventoryExpiry(inventoryProvisional);
  const handleRetryInventory = useCallback(() => {
    restartDiscovery();
    onRetryInventory?.();
  }, [onRetryInventory, restartDiscovery]);

  if (tracks.length === 0 && !inventoryProvisional) return null;

  const isInteractive = Boolean(onSelectTrack || onResetSelection);
  const declared = inventoryDeclared && !inventoryProvisional;
  // A declaration with no language or codec names no track. Group it as the
  // release listing instead of presenting a fabricated row as a probe result.
  const declaredHasDetail = tracks.some((track) =>
    Boolean(track.language || track.codec || track.title || track.embedded_title),
  );
  const declaredGroupOnly = declared && tracks.length > 0 && !declaredHasDetail;

  const { autoTrack, activeTrack } = resolveAudioTrackSelection(
    version,
    selectionMode === "explicit" ? explicitTrackIndex : null,
  );
  const activeSummary = formatAudioTrackSummary(activeTrack);
  const autoSummary = formatAudioTrackSummary(autoTrack);

  return (
    <DetailPopover
      positionKey={`${inventoryProvisional}:${discoveryExpired}:${tracks.length}`}
      contentClassName="w-80 p-1.5"
      trigger={
        <Button
          variant="glass"
          className="h-8 max-w-full min-w-0 shrink gap-1.5 rounded-full px-3 text-xs font-medium"
        >
          <Volume2 className="size-3.5" />
          Audio
          <span className="text-muted-foreground max-w-44 truncate text-[0.6875rem] font-normal sm:max-w-64">
            {inventoryProvisional
              ? discoveryExpired
                ? "Unavailable"
                : "Discovering…"
              : declaredGroupOnly
                ? "Release listing"
                : isInteractive
                  ? selectionMode === "auto"
                    ? `Auto: ${autoSummary}`
                    : activeSummary
                  : tracks.length}
          </span>
          <ChevronDown className="text-muted-foreground size-3" />
        </Button>
      }
    >
      <div className="space-y-0.5">
        {isInteractive && (
          <AudioOptionRow
            active={selectionMode === "auto"}
            title="Auto"
            description={inventoryProvisional ? "Profile defaults" : autoSummary}
            onSelect={onResetSelection}
          />
        )}
        {declared && !declaredGroupOnly && (
          <div className="text-muted-foreground px-3 py-1 text-[0.6875rem]">
            From release listing
          </div>
        )}
        {declaredGroupOnly && (
          <div className="text-muted-foreground px-3 py-2 text-xs">
            Audio · from release listing
          </div>
        )}
        {inventoryProvisional && !discoveryExpired && (
          <div className="text-muted-foreground flex items-center gap-2 px-3 py-2 text-xs">
            <Loader2 className="size-3 animate-spin" />
            Discovering audio tracks…
          </div>
        )}
        {inventoryProvisional && discoveryExpired && (
          <div className="text-muted-foreground flex items-center justify-between gap-2 px-3 py-2 text-xs">
            <span>Audio tracks unavailable.</span>
            <button
              type="button"
              onClick={handleRetryInventory}
              className="text-primary shrink-0 rounded px-1.5 py-0.5 font-medium hover:underline focus-visible:ring-2 focus-visible:outline-none"
            >
              Retry
            </button>
          </div>
        )}
        {(inventoryProvisional || declaredGroupOnly ? [] : tracks).map((track, index) => {
          const codec = audioTrackLabel(track);
          const channels = formatChannels(track.channels);
          const language = getLanguageName(track.language ?? "");
          const fallbackTitle = audioTitle(track);
          const title = language || fallbackTitle;
          const embeddedTitle = track.title?.trim() || track.embedded_title?.trim() || "";
          const meta = [
            embeddedTitle && embeddedTitle !== title ? embeddedTitle : "",
            compactAudioMeta(track),
          ]
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
                    <Badge variant="secondary" className="px-1.5 py-0 text-[0.625rem] uppercase">
                      {codec}
                    </Badge>
                  )}
                  {channels && (
                    <Badge variant="secondary" className="px-1.5 py-0 text-[0.625rem]">
                      {channels}
                    </Badge>
                  )}
                  {track.default && (
                    <Badge variant="outline" className="px-1.5 py-0 text-[0.625rem] uppercase">
                      Default
                    </Badge>
                  )}
                </>
              }
            />
          );
        })}
      </div>
    </DetailPopover>
  );
}

export default memo(AudioTracksPopover);
