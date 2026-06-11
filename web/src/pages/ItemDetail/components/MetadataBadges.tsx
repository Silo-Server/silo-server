interface MetadataBadgesProps {
  year?: string;
  contentRating?: string;
  duration?: string;
  seasonCount?: number;
  episodeCount?: number;
  volumeCount?: number;
  chapterCount?: number;
  status?: string;
}

export default function MetadataBadges({
  year,
  contentRating,
  duration,
  seasonCount,
  episodeCount,
  volumeCount,
  chapterCount,
  status,
}: MetadataBadgesProps) {
  return (
    <div className="flex flex-wrap items-center gap-2">
      {year && <span className="metadata-badge">{year}</span>}
      {contentRating && <span className="metadata-badge">{contentRating}</span>}
      {duration && <span className="metadata-badge">{duration}</span>}
      {seasonCount != null && (
        <span className="metadata-badge">
          {seasonCount} {seasonCount === 1 ? "Season" : "Seasons"}
        </span>
      )}
      {episodeCount != null && (
        <span className="metadata-badge">
          {episodeCount} {episodeCount === 1 ? "Episode" : "Episodes"}
        </span>
      )}
      {volumeCount != null && volumeCount > 0 && (
        <span className="metadata-badge">
          {volumeCount} {volumeCount === 1 ? "Volume" : "Volumes"}
        </span>
      )}
      {chapterCount != null && chapterCount > 0 && (
        <span className="metadata-badge">
          {chapterCount} {chapterCount === 1 ? "Chapter" : "Chapters"}
        </span>
      )}
      {status && (
        <span className="metadata-badge border-primary/25 text-primary bg-primary/10">
          {status}
        </span>
      )}
    </div>
  );
}
