import {
  useCatalogItemEpisodes,
  useCatalogSeasonDetail,
  useCatalogSeasonEpisodes,
  useCatalogSeriesSeasons,
} from "./catalogRead";

// libraryId names the library the page was opened from; see
// "Library-scoped version lists" in docs/catalog-api.md.

export function useSeasons(seriesId: string | undefined, libraryId?: number) {
  return useCatalogSeriesSeasons(seriesId, libraryId);
}

export function useSeasonEpisodes(
  seriesId: string | undefined,
  seasonNum: number,
  libraryId?: number,
) {
  return useCatalogSeasonEpisodes(seriesId, seasonNum, libraryId);
}

export function useItemEpisodes(itemId: string | undefined, libraryId?: number) {
  return useCatalogItemEpisodes(itemId, libraryId);
}

export function useSeasonDetail(
  seriesId: string | undefined,
  seasonNum: number,
  libraryId?: number,
) {
  return useCatalogSeasonDetail(seriesId, seasonNum, libraryId);
}
