import { Play } from "lucide-react";
import type { FileVersion } from "@/api/types";
import {
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
} from "@/components/ui/dropdown-menu";
import { formatFileSize, mapAudioLabel } from "@/lib/mediaFormat";
import { videoRangeLabel } from "@/lib/videoRange";
import { extractSourceHint } from "./versionFormatUtils";
import { audioScore, resolutionScore } from "./versionRankingUtils";

// ---------------------------------------------------------------------------
// Exported helper functions (also used by tests)
// ---------------------------------------------------------------------------

export function buildQualitySummary(version: FileVersion): string {
  if (version.file_path) {
    try {
      const parsed = new URL(version.file_path, "http://silo.local");
      if (parsed.searchParams.get("results") === "all" && !parsed.searchParams.has("result")) {
        return "More results…";
      }
    } catch {
      // Fall through to the regular media quality summary for malformed paths.
    }
  }
  const parts: string[] = [];

  if (version.resolution) parts.push(version.resolution);

  const textToScan = [version.file_name, version.edition_raw].filter(Boolean).join(" ");
  const sourceHint = textToScan ? extractSourceHint(textToScan) : null;
  if (sourceHint) parts.push(sourceHint);

  if (version.codec_video) parts.push(version.codec_video.toUpperCase());
  const rangeLabel = videoRangeLabel(version);
  if (rangeLabel) parts.push(rangeLabel);
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

  const textToScan = [version.file_name, version.edition_raw].filter(Boolean).join(" ");
  const hint = textToScan ? extractSourceHint(textToScan) : null;
  if (hint) parts.push(hint);

  return parts.join(" · ");
}

export function sortByResolution(versions: FileVersion[]): FileVersion[] {
  return [...versions].sort(
    (a, b) =>
      resolutionScore(b.resolution) - resolutionScore(a.resolution) ||
      Number(b.hdr) - Number(a.hdr) ||
      audioScore(b.codec_audio) - audioScore(a.codec_audio) ||
      (b.file_size ?? 0) - (a.file_size ?? 0),
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
              <span className="text-foreground block truncate text-sm font-semibold">
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
