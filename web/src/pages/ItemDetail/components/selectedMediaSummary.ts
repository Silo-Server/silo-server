import type { FileVersion, PlaybackVariant } from "@/api/types";
import { videoRangeLabel } from "@/lib/videoRange";
import { pickBestAttributes } from "./versionRankingUtils";

export interface SelectedMediaSummary {
  durationMinutes: number;
  resolution: string;
  videoRangeLabel: string;
  audioLabel: string;
}

export function resolveSelectedMediaSummary(
  selectedVersion: FileVersion | null,
  playbackVariants: PlaybackVariant[] | undefined,
  fallbackRuntimeMinutes: number,
): SelectedMediaSummary {
  const quality = selectedVersion ? pickBestAttributes([selectedVersion]) : null;
  const selectedVariant = selectedVersion
    ? playbackVariants?.find((variant) =>
        (variant.parts ?? []).some((part) =>
          (part.versions ?? []).some((version) => version.file_id === selectedVersion.file_id),
        ),
      )
    : undefined;
  const isMultipart =
    (selectedVariant?.part_count ?? 0) > 1 || (selectedVariant?.parts?.length ?? 0) > 1;
  const variantDuration = selectedVariant?.total_duration ?? 0;
  const durationSeconds =
    isMultipart && variantDuration > 0 ? variantDuration : (selectedVersion?.duration ?? 0);

  return {
    durationMinutes:
      durationSeconds > 0 ? Math.round(durationSeconds / 60) : fallbackRuntimeMinutes,
    resolution: quality?.resolution ?? "",
    videoRangeLabel: selectedVersion ? videoRangeLabel(selectedVersion) : "",
    audioLabel: quality?.audioLabel ?? "",
  };
}
