import { Play } from "lucide-react";
import type { FileVersion } from "@/api/types";
import {
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
} from "@/components/ui/dropdown-menu";
import { formatFileSize, extractSourceHint } from "./versionFormatUtils";
import { resolutionScore, mapAudioLabel } from "./versionRankingUtils";

// ---------------------------------------------------------------------------
// Exported helper functions (also used by tests)
// ---------------------------------------------------------------------------

export function buildQualitySummary(version: FileVersion): string {
  const parts: string[] = [];

  if (version.resolution) parts.push(version.resolution);
  if (version.codec_video) parts.push(version.codec_video.toUpperCase());
  if (version.hdr) parts.push("HDR");
  if (version.codec_audio) parts.push(mapAudioLabel(version.codec_audio));
  if (parts.length === 0 && version.container) {
    parts.push(version.container.toUpperCase());
  }

  return parts.join(" · ");
}

export function buildDetailLine(version: FileVersion): string {
  const parts: string[] = [];

  const size = formatFileSize(version.file_size);
  if (size) parts.push(size);

  const hint = version.file_name ? extractSourceHint(version.file_name) : null;
  if (hint) parts.push(hint);

  return parts.join(" · ");
}

export function sortByResolution(versions: FileVersion[]): FileVersion[] {
  return [...versions].sort(
    (a, b) => resolutionScore(b.resolution) - resolutionScore(a.resolution),
  );
}

// ---------------------------------------------------------------------------
// VersionFlyoutItems (default export)
// ---------------------------------------------------------------------------

interface VersionFlyoutItemsProps {
  versions: FileVersion[];
  onPlayVersion: (fileId: number) => void;
}

export default function VersionFlyoutItems({ versions, onPlayVersion }: VersionFlyoutItemsProps) {
  const sorted = sortByResolution(versions);

  return (
    <>
      <DropdownMenuLabel>Play Version</DropdownMenuLabel>
      <DropdownMenuSeparator />

      {sorted.map((version) => {
        const qualitySummary = buildQualitySummary(version);
        const detailLine = buildDetailLine(version);

        return (
          <DropdownMenuItem
            key={version.file_id}
            className="flex items-center gap-3 rounded-lg py-2.5"
            onSelect={() => onPlayVersion(version.file_id)}
          >
            <span className="bg-accent/70 flex size-7 shrink-0 items-center justify-center rounded-full">
              <Play className="text-foreground size-3.5 fill-current" />
            </span>

            <span className="min-w-0 flex-1">
              <span className="text-foreground block truncate text-sm font-medium">
                {qualitySummary}
              </span>
              {detailLine && (
                <span className="text-muted-foreground block text-xs">{detailLine}</span>
              )}
            </span>
          </DropdownMenuItem>
        );
      })}
    </>
  );
}
