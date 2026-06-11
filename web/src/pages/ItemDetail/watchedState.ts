import type { QueryClient } from "@tanstack/react-query";
import type { EpisodesResponse, ItemDetail, SeasonsResponse } from "@/api/types";
import { catalogKeys, ebookKeys, episodeKeys, itemKeys, progressKeys } from "@/hooks/queries/keys";

type WatchedStateItem = Pick<
  ItemDetail,
  "content_id" | "type" | "series_id" | "season_number" | "user_data"
>;

function appendUniqueKey(keys: Array<readonly unknown[]>, nextKey: readonly unknown[]) {
  if (
    keys.some(
      (existingKey) =>
        existingKey.length === nextKey.length &&
        existingKey.every((part, index) => Object.is(part, nextKey[index])),
    )
  ) {
    return;
  }
  keys.push(nextKey);
}

export function getWatchedActionLabel(item: Pick<ItemDetail, "type" | "user_data">): string {
  const played = item.user_data?.played === true;

  switch (item.type) {
    case "series":
      return played ? "Mark Series Unwatched" : "Mark Series Watched";
    case "season":
      return played ? "Mark Season Unwatched" : "Mark Season Watched";
    case "audiobook":
      return played ? "Mark Unlistened" : "Mark Listened";
    case "ebook":
    case "manga":
      return played ? "Mark Unread" : "Mark Read";
    case "episode":
      return played ? "Mark Unwatched" : "Mark Watched";
    default:
      return played ? "Mark Unwatched" : "Mark Watched";
  }
}

export function getWatchedToastMessage(item: Pick<ItemDetail, "type">, played: boolean): string {
  switch (item.type) {
    case "audiobook":
      return played ? "Marked as listened" : "Marked as unlistened";
    case "ebook":
    case "manga":
      return played ? "Marked as read" : "Marked as unread";
    default:
      return played ? "Marked as watched" : "Marked as unwatched";
  }
}

export function getWatchedInvalidationKeys(item: WatchedStateItem) {
  const keys: Array<readonly unknown[]> = [
    catalogKeys.itemDetail(item.content_id),
    itemKeys.detail(item.content_id),
    progressKeys.all,
  ];

  if (item.series_id) {
    keys.push(catalogKeys.itemDetail(item.series_id));
    keys.push(itemKeys.detail(item.series_id));
  }

  if (item.type === "series") {
    keys.push(catalogKeys.seriesSeasons(item.content_id));
    keys.push(episodeKeys.seasons(item.content_id));
  }

  if (item.type === "season" && item.series_id) {
    keys.push(catalogKeys.seriesSeasons(item.series_id));
    keys.push(episodeKeys.seasons(item.series_id));
  }

  if (item.series_id && item.season_number != null) {
    keys.push(catalogKeys.seasonDetail(item.series_id, item.season_number));
    keys.push(catalogKeys.seasonEpisodes(item.series_id, item.season_number));
    keys.push(episodeKeys.seasonDetail(item.series_id, item.season_number));
    keys.push(episodeKeys.bySeason(item.series_id, item.season_number));
  }

  if (item.type === "episode" || item.type === "season" || item.type === "series") {
    keys.push(itemKeys.details());
    keys.push(episodeKeys.all);
  }

  if (item.type === "season") {
    keys.push(catalogKeys.itemEpisodes(item.content_id));
    keys.push(episodeKeys.byItem(item.content_id));
  }

  if (item.type === "ebook") {
    keys.push(ebookKeys.readerProgress(item.content_id));
  }

  return keys;
}

export function getCachedWatchedInvalidationKeys(queryClient: QueryClient, item: WatchedStateItem) {
  const keys = [...getWatchedInvalidationKeys(item)];

  if (item.type !== "series") {
    return keys;
  }

  const seasonsData = queryClient.getQueryData<SeasonsResponse>(
    catalogKeys.seriesSeasons(item.content_id),
  );

  for (const season of seasonsData?.seasons ?? []) {
    appendUniqueKey(keys, catalogKeys.itemDetail(season.content_id));
    appendUniqueKey(keys, itemKeys.detail(season.content_id));
    appendUniqueKey(keys, catalogKeys.itemEpisodes(season.content_id));
    appendUniqueKey(keys, episodeKeys.byItem(season.content_id));
    appendUniqueKey(keys, catalogKeys.seasonDetail(item.content_id, season.season_number));
    appendUniqueKey(keys, catalogKeys.seasonEpisodes(item.content_id, season.season_number));
    appendUniqueKey(keys, episodeKeys.seasonDetail(item.content_id, season.season_number));
    appendUniqueKey(keys, episodeKeys.bySeason(item.content_id, season.season_number));

    const episodesData =
      queryClient.getQueryData<EpisodesResponse>(catalogKeys.itemEpisodes(season.content_id)) ??
      queryClient.getQueryData<EpisodesResponse>(
        catalogKeys.seasonEpisodes(item.content_id, season.season_number),
      );

    for (const episode of episodesData?.episodes ?? []) {
      appendUniqueKey(keys, catalogKeys.itemDetail(episode.content_id));
      appendUniqueKey(keys, itemKeys.detail(episode.content_id));
    }
  }

  return keys;
}
