interface EpisodeCardLabelItem {
  type?: string;
  title?: string;
  series_title?: string;
  season_number?: number | null;
  episode_number?: number | null;
}

export interface EpisodeCardLabels {
  seriesTitle: string;
  episodeTitle?: string;
  episodeCode: string;
}

function formatEpisodeCode(seasonNumber: number, episodeNumber: number): string {
  return `S${String(seasonNumber).padStart(2, "0")}E${String(episodeNumber).padStart(2, "0")}`;
}

export function buildEpisodeCardLabels(item: EpisodeCardLabelItem): EpisodeCardLabels | null {
  if (
    item.type !== "episode" ||
    item.season_number == null ||
    item.episode_number == null ||
    !item.title
  ) {
    return null;
  }

  const hasSeriesTitle = Boolean(item.series_title && item.series_title !== item.title);

  return {
    seriesTitle: hasSeriesTitle ? item.series_title! : item.title,
    episodeTitle: hasSeriesTitle ? item.title : undefined,
    episodeCode: formatEpisodeCode(item.season_number, item.episode_number),
  };
}
