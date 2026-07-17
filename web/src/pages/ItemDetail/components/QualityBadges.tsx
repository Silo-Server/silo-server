import type { SelectedMediaSummary } from "./selectedMediaSummary";

interface QualityBadgesProps {
  summary: SelectedMediaSummary;
}

export default function QualityBadges({ summary }: QualityBadgesProps) {
  const badges: string[] = [];
  if (summary.resolution) badges.push(summary.resolution);
  if (summary.videoRangeLabel) badges.push(summary.videoRangeLabel);
  if (summary.audioLabel) badges.push(summary.audioLabel);

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
