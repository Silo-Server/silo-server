import type { FileVersion } from "@/api/types";
import { bestVideoRangeLabel } from "@/lib/videoRange";
import { pickBestAttributes } from "./versionRankingUtils";

interface QualityBadgesProps {
  versions: FileVersion[];
}

export default function QualityBadges({ versions }: QualityBadgesProps) {
  const best = pickBestAttributes(versions);
  if (!best) return null;

  const badges: string[] = [];
  if (best.resolution) badges.push(best.resolution);
  const rangeLabel = bestVideoRangeLabel(versions);
  if (rangeLabel) badges.push(rangeLabel);
  if (best.audioLabel) badges.push(best.audioLabel);

  if (badges.length === 0) return null;

  return (
    <div className="flex flex-wrap items-center gap-2">
      {badges.map((label) => (
        <span
          key={label}
          className="bg-primary/10 text-primary border-primary/20 inline-flex items-center rounded-sm border px-2 py-0.5 text-[11px] font-semibold tracking-wide uppercase"
        >
          {label}
        </span>
      ))}
    </div>
  );
}
