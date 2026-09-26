import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import getCatalogItemOk from "../../../../contracts/api/v2/fixtures/get_catalog_item_ok.json";
import getSeriesSeasonOk from "../../../../contracts/api/v2/fixtures/get_series_season_ok.json";
import listCatalogItemEpisodesOk from "../../../../contracts/api/v2/fixtures/list_catalog_item_episodes_ok.json";
import listCatalogItemMangaFilesOk from "../../../../contracts/api/v2/fixtures/list_catalog_item_manga_files_ok.json";
import listCatalogItemVersionsOk from "../../../../contracts/api/v2/fixtures/list_catalog_item_versions_ok.json";
import listSeasonEpisodesOk from "../../../../contracts/api/v2/fixtures/list_season_episodes_ok.json";
import listSeriesSeasonsOk from "../../../../contracts/api/v2/fixtures/list_series_seasons_ok.json";

import { setProfileId } from "@/api/client";
import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";

import {
  fetchCatalogItemDetail,
  fetchCatalogItemEpisodes,
  fetchCatalogItemVersions,
  fetchCatalogSeasonDetail,
  fetchCatalogSeasonEpisodes,
  fetchCatalogSeriesSeasons,
  fetchMangaSeriesFiles,
  useCatalogItemDetail,
} from "./catalogRead";

type FetchMock = ReturnType<typeof vi.fn<typeof fetch>>;

function stubFetch(body: unknown): FetchMock {
  const fetchMock = vi.fn<typeof fetch>(async () => jsonResponse(body));
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function requestedUrl(fetchMock: FetchMock, index = 0): string {
  return String(fetchMock.mock.calls[index]?.[0]);
}

describe("catalog item reads on the v2 contract", () => {
  beforeEach(() => {
    installPolicyStorageMocks();
    setProfileId("p-owner");
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("reads the item detail and keeps the v1 detail shape for the page", async () => {
    const fetchMock = stubFetch(getCatalogItemOk);

    const detail = await fetchCatalogItemDetail("movie:heat-1995", 12);

    expect(requestedUrl(fetchMock)).toBe("/api/v2/catalog/items/movie%3Aheat-1995?library_id=12");
    expect((fetchMock.mock.calls[0]?.[1]?.headers as Record<string, string>)["X-Profile-Id"]).toBe(
      "p-owner",
    );
    expect(detail.content_id).toBe("movie:heat-1995");
    expect(detail.title).toBe("Heat");
    expect(detail.year).toBe(1995);
    // v2 omits absent strings; the page still reads v1's "" / null spellings.
    expect(detail.overview).toBe("");
    expect(detail.poster_url).toBe("");
    expect(detail.rating_imdb).toBeNull();
    expect(detail.release_date).toBeNull();
    expect(detail.intro).toBeNull();
    expect(detail.user_state).toEqual({ played: true, is_favorite: true, in_watchlist: false });
    expect(detail.status).toBeUndefined();
  });

  it("preserves explicit subtitle choices and track identity in item detail", async () => {
    const signature = {
      source: "embedded",
      language: "eng",
      codec: "subrip",
      label: "English SDH",
      forced: false,
      hearing_impaired: true,
    };
    stubFetch({
      ...getCatalogItemOk,
      effective_subtitle_language: "eng",
      effective_subtitle_mode: "off",
      effective_show_forced_subtitles: false,
      effective_subtitle_track_signature: signature,
      subtitles: [{ index: 3, language: "eng", forced: false }],
    });

    const detail = await fetchCatalogItemDetail("movie:heat-1995");

    expect(detail.effective_subtitle_language).toBe("eng");
    expect(detail.effective_subtitle_mode).toBe("off");
    expect(detail.effective_show_forced_subtitles).toBe(false);
    expect(detail.effective_subtitle_track_signature).toEqual(signature);
    expect(detail.subtitles[0]).toMatchObject({ codec: "", title: "", forced: false });
  });

  it("aborts the detail fetch when its last observer unmounts", async () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    let requestSignal: AbortSignal | null | undefined;
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async (_url, options) => {
        requestSignal = options?.signal;
        return new Promise<Response>((_resolve, reject) => {
          requestSignal?.addEventListener(
            "abort",
            () => reject(new DOMException("Aborted", "AbortError")),
            { once: true },
          );
        });
      }),
    );
    const wrapper = ({ children }: { children: ReactNode }) =>
      createElement(QueryClientProvider, { client: queryClient }, children);
    const { unmount } = renderHook(() => useCatalogItemDetail("movie:heat-1995", 12), { wrapper });

    await waitFor(() => expect(requestSignal).toBeDefined());
    expect(requestSignal?.aborted).toBe(false);
    unmount();
    expect(requestSignal?.aborted).toBe(true);
    queryClient.clear();
  });

  it("cancels an in-flight detail request through the transport", async () => {
    const controller = new AbortController();
    const aborted = new DOMException("The request was aborted", "AbortError");
    const fetchMock = vi.fn<typeof fetch>(async (_url, options) => {
      expect(options?.signal).toBe(controller.signal);
      return new Promise<Response>((_resolve, reject) => {
        options?.signal?.addEventListener("abort", () => reject(aborted), { once: true });
      });
    });
    vi.stubGlobal("fetch", fetchMock);

    const result = fetchCatalogItemDetail("movie:heat-1995", 12, { signal: controller.signal });
    const rejection = expect(result).rejects.toBe(aborted);
    controller.abort();

    await rejection;
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("encodes item ids and drops the library filter when none is given", async () => {
    const fetchMock = stubFetch(getCatalogItemOk);

    await fetchCatalogItemDetail("ebook 1/isbn:978");

    expect(requestedUrl(fetchMock)).toBe("/api/v2/catalog/items/ebook%201%2Fisbn%3A978");
  });

  it("unwraps the versions collection and returns numeric file ids", async () => {
    const fetchMock = stubFetch(listCatalogItemVersionsOk);

    const versions = await fetchCatalogItemVersions("movie:heat-1995");

    expect(requestedUrl(fetchMock)).toBe("/api/v2/catalog/items/movie%3Aheat-1995/versions");
    expect(versions).toHaveLength(1);
    expect(versions[0]?.file_id).toBe(120);
    expect(versions[0]?.resolution).toBe("2160p");
  });

  it("unwraps the episode collection into the v1 episodes envelope", async () => {
    const fetchMock = stubFetch(listCatalogItemEpisodesOk);

    const { episodes } = await fetchCatalogItemEpisodes("series:severance-S01");

    expect(requestedUrl(fetchMock)).toBe("/api/v2/catalog/items/series%3Aseverance-S01/episodes");
    expect(episodes.map((e) => e.content_id)).toEqual([
      "episode:severance-s01e01",
      "episode:severance-s01e02",
    ]);
    expect(episodes[0]?.overview).toBe("");
    expect(episodes[0]?.still_url).toBe("");
    expect(episodes[0]?.files[0]?.file_id).toBe(201);
    expect(episodes[0]?.user_data?.played).toBe(true);
  });

  it("unwraps the season collection and the single season read", async () => {
    const seasonsMock = stubFetch(listSeriesSeasonsOk);
    const { seasons } = await fetchCatalogSeriesSeasons("series:severance", 3);

    expect(requestedUrl(seasonsMock)).toBe(
      "/api/v2/catalog/series/series%3Aseverance/seasons?library_id=3",
    );
    expect(seasons[0]?.season_number).toBe(1);
    expect(seasons[0]?.is_specials).toBe(false);
    expect(seasons[0]?.overview).toBe("");
    expect(seasons[0]?.user_data?.unplayed_count).toBe(8);

    const seasonMock = stubFetch(getSeriesSeasonOk);
    const { season } = await fetchCatalogSeasonDetail("series:severance", 1);

    expect(requestedUrl(seasonMock)).toBe("/api/v2/catalog/series/series%3Aseverance/seasons/1");
    expect(season.content_id).toBe("series:severance-S01");
    expect(season.poster_url).toBe("");
  });

  it("reads one season's episodes", async () => {
    const fetchMock = stubFetch(listSeasonEpisodesOk);

    const { episodes } = await fetchCatalogSeasonEpisodes("series:severance", 1);

    expect(requestedUrl(fetchMock)).toBe(
      "/api/v2/catalog/series/series%3Aseverance/seasons/1/episodes",
    );
    expect(episodes.length).toBeGreaterThan(0);
    expect(episodes[0]?.season_number).toBe(1);
  });

  it("maps the manga file descriptors onto the dialog's files shape", async () => {
    const fetchMock = stubFetch(listCatalogItemMangaFilesOk);

    const files = await fetchMangaSeriesFiles("manga:berserk");

    expect(requestedUrl(fetchMock)).toBe("/api/v2/catalog/items/manga%3Aberserk/manga-files");
    expect(files.folder_paths).toEqual(["/media/manga/Berserk"]);
    expect(files.files.map((f) => f.file_name)).toEqual(["c001.cbz"]);
  });
});
