import {
  useCatalogItemEpisodes,
  useCatalogSeasonDetail,
  useCatalogSeasonEpisodes,
  useCatalogSeriesSeasons,
  type MissingEpisodes,
} from "./catalogRead";

export function useSeasons(seriesId: string | undefined, missing: MissingEpisodes = "none") {
  return useCatalogSeriesSeasons(seriesId, undefined, missing);
}

export function useSeasonEpisodes(seriesId: string | undefined, seasonNum: number) {
  return useCatalogSeasonEpisodes(seriesId, seasonNum);
}

export function useItemEpisodes(itemId: string | undefined, missing: MissingEpisodes = "none") {
  return useCatalogItemEpisodes(itemId, undefined, missing);
}

export function useSeasonDetail(seriesId: string | undefined, seasonNum: number) {
  return useCatalogSeasonDetail(seriesId, seasonNum);
}
