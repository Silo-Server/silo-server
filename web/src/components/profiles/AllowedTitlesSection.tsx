import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Plus, Search, X } from "lucide-react";
import type { BrowseItem } from "@/api/types";
import { PosterArt } from "@/components/calm/PosterTile";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { createCatalogSearchState, fetchCatalogPage } from "@/hooks/queries/catalog";
import { catalogKeys } from "@/hooks/queries/keys";
import {
  useAllowedCollections,
  useAllowedTitles,
  useSetCollectionAllowance,
  useSetTitleAllowance,
} from "@/hooks/queries/titleAllowances";
import { useDebounce } from "@/hooks/useDebounce";
import { useT, type MessageKey } from "@/i18n";
import { formatLocale } from "@/lib/appLocale";

const SEARCH_LIMIT = 10;
const SEARCH_DEBOUNCE_MS = 250;

/**
 * A list that could not load, with a retry: in place of a wrong empty state
 * when nothing loaded (`stale` false), or above the cached list when only a
 * refresh failed (`stale` true).
 */
function LoadError({
  staleKey,
  failedKey,
  stale,
  onRetry,
}: {
  staleKey: MessageKey;
  failedKey: MessageKey;
  stale: boolean;
  onRetry: () => void;
}) {
  const t = useT();
  return (
    <p role="alert" className="text-muted-foreground flex items-center gap-2 text-sm">
      {stale ? t(staleKey) : t(failedKey)}
      <Button type="button" variant="link" size="sm" onClick={onRetry}>
        {t("common.retry")}
      </Button>
    </p>
  );
}

/**
 * The profile editor's "Allowed titles" and "Allowed collections": what the
 * household's primary profile lets this limited profile watch above its
 * limit. Changes save at once, separately from the profile form.
 */
export function AllowedTitlesSection({ profileId }: { profileId: string }) {
  const t = useT();
  const titles = useAllowedTitles(profileId, true);
  const collections = useAllowedCollections(profileId, true);
  const setTitle = useSetTitleAllowance();
  const setCollection = useSetCollectionAllowance();
  const allowedTitles = titles.data?.pages.flatMap((page) => page.items);
  // Marks the search results already allowed; titles on pages not loaded yet
  // show "Allow", and allowing one again changes nothing.
  const allowedIds = new Set((allowedTitles ?? []).map((item) => item.content_id));

  return (
    <section className="border-border space-y-4 rounded-md border p-4">
      <div className="space-y-1">
        <h3 className="text-sm font-semibold">{t("web.profiles.allowedTitles.heading")}</h3>
        <p className="text-muted-foreground text-sm">{t("web.profiles.allowedTitles.hint")}</p>
      </div>

      <AddAllowedTitle
        allowedIds={allowedIds}
        onPick={(item) => setTitle.mutate({ profileId, itemId: item.content_id, allowed: true })}
      />

      {titles.isError ? (
        <LoadError
          staleKey="web.profiles.allowedTitles.titlesStale"
          failedKey="web.profiles.allowedTitles.titlesFailed"
          stale={titles.data !== undefined}
          onRetry={() => void titles.refetch()}
        />
      ) : null}
      {titles.isLoading ? (
        <p className="text-muted-foreground text-sm">{t("web.profiles.allowedTitles.loading")}</p>
      ) : titles.isError && !allowedTitles ? null : (allowedTitles ?? []).length === 0 ? (
        // Cards the acting profile can't see are left out of each page, so
        // an empty page is an empty list only when no page follows.
        titles.hasNextPage ? null : (
          <p className="text-muted-foreground text-sm">{t("web.profiles.allowedTitles.none")}</p>
        )
      ) : (
        <ul
          aria-label={t("web.profiles.allowedTitles.heading")}
          className="grid gap-2 sm:grid-cols-2"
        >
          {(allowedTitles ?? []).map((item) => (
            <li
              key={item.content_id}
              className="border-border flex items-center gap-3 rounded-md border px-2 py-2"
            >
              <PosterArt
                posterUrl={item.poster_url || undefined}
                thumbhash={item.poster_thumbhash}
                className="ring-border/60 aspect-[2/3] w-9 shrink-0 rounded-md ring-1 ring-inset"
              />
              <span className="min-w-0 flex-1 truncate text-sm">
                {item.title}
                {item.year ? (
                  <span className="text-muted-foreground ms-1.5 text-xs">{item.year}</span>
                ) : null}
              </span>
              <Button
                type="button"
                variant="ghost"
                size="icon"
                aria-label={t("web.profiles.allowedTitles.stopAllowing", { name: item.title })}
                disabled={setTitle.isPending}
                onClick={() =>
                  setTitle.mutate({ profileId, itemId: item.content_id, allowed: false })
                }
              >
                <X className="size-4" />
              </Button>
            </li>
          ))}
        </ul>
      )}
      {titles.hasNextPage ? (
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={titles.isFetchingNextPage}
          onClick={() => void titles.fetchNextPage()}
        >
          {titles.isFetchingNextPage
            ? t("web.profiles.allowedTitles.loading")
            : t("web.profiles.allowedTitles.showMore")}
        </Button>
      ) : null}

      {collections.isError ? (
        <LoadError
          staleKey="web.profiles.allowedTitles.collectionsStale"
          failedKey="web.profiles.allowedTitles.collectionsFailed"
          stale={collections.data !== undefined}
          onRetry={() => void collections.refetch()}
        />
      ) : null}
      {(collections.data ?? []).length > 0 ? (
        <div className="space-y-2">
          <h4 className="text-sm font-medium">
            {t("web.profiles.allowedTitles.collectionsHeading")}
          </h4>
          <ul
            aria-label={t("web.profiles.allowedTitles.collectionsHeading")}
            className="grid gap-2"
          >
            {(collections.data ?? []).map((collection) => (
              <li
                key={collection.collection_id}
                className="border-border flex items-center gap-3 rounded-md border px-3 py-2"
              >
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-sm">{collection.name}</span>
                  <span className="text-muted-foreground text-xs">
                    {collection.live
                      ? t("web.profiles.allowedTitles.titleCount", {
                          count: collection.item_count,
                          count_text: collection.item_count.toLocaleString(formatLocale()),
                        })
                      : t("web.profiles.allowedTitles.titleCountPaused", {
                          count: collection.item_count,
                          count_text: collection.item_count.toLocaleString(formatLocale()),
                        })}
                  </span>
                </span>
                <Button
                  type="button"
                  variant="ghost"
                  size="icon"
                  aria-label={t("web.profiles.allowedTitles.stopAllowing", {
                    name: collection.name,
                  })}
                  disabled={setCollection.isPending}
                  onClick={() =>
                    setCollection.mutate({
                      profileId,
                      collectionId: collection.collection_id,
                      allowed: false,
                    })
                  }
                >
                  <X className="size-4" />
                </Button>
              </li>
            ))}
          </ul>
        </div>
      ) : null}
    </section>
  );
}

/** A catalog search whose picks become allowances. Only movies and series are offered. */
function AddAllowedTitle({
  allowedIds,
  onPick,
}: {
  allowedIds: Set<string>;
  onPick: (item: BrowseItem) => void;
}) {
  const t = useT();
  const [query, setQuery] = useState("");
  const debounced = useDebounce(query.trim(), SEARCH_DEBOUNCE_MS);
  const searchState = useMemo(
    () => createCatalogSearchState("query", { q: debounced || undefined }),
    [debounced],
  );
  const results = useQuery({
    queryKey: [
      "allowedTitlePicker",
      catalogKeys.list({
        source: searchState.source,
        q: searchState.q,
        limit: SEARCH_LIMIT,
        offset: 0,
      }),
    ],
    queryFn: ({ signal }) => fetchCatalogPage(searchState, SEARCH_LIMIT, 0, { signal }),
    enabled: debounced.length > 0,
    staleTime: 30 * 1000,
  });
  const found = (results.data?.items ?? []).filter(
    (item) => item.type === "movie" || item.type === "series",
  );

  // A failed refresh keeps the results it already has and offers a retry;
  // only a search with nothing to show reports the failure in their place.
  const hasResults = results.data !== undefined;
  let status: string | null = null;
  if (debounced.length > 0) {
    if (results.isLoading) status = t("web.profiles.allowedTitles.searching");
    else if (results.isError && !hasResults) status = t("web.profiles.allowedTitles.searchFailed");
    else if (found.length === 0) status = t("web.profiles.allowedTitles.noMatch");
  }
  const staleResults = results.isError && hasResults;

  return (
    <div className="grid gap-2">
      <div className="relative">
        <Search
          aria-hidden
          className="text-muted-foreground absolute start-3 top-1/2 size-4 -translate-y-1/2"
        />
        <Input
          type="search"
          aria-label={t("web.profiles.allowedTitles.searchLabel")}
          placeholder={t("web.profiles.allowedTitles.searchPlaceholder")}
          className="ps-9"
          autoComplete="off"
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          // The section sits inside the profile form: Enter must not save it.
          onKeyDown={(event) => {
            if (event.key === "Enter") event.preventDefault();
          }}
        />
      </div>
      {status ? (
        <p role="status" className="text-muted-foreground text-[13px]">
          {status}
        </p>
      ) : null}
      {staleResults ? (
        <p role="status" className="text-muted-foreground flex items-center gap-2 text-[13px]">
          {t("web.profiles.allowedTitles.resultsStale")}
          <Button type="button" variant="link" size="sm" onClick={() => void results.refetch()}>
            {t("common.retry")}
          </Button>
        </p>
      ) : null}
      {!status && debounced.length > 0 ? (
        <ul
          aria-label={t("web.profiles.allowedTitles.results")}
          className="border-border divide-border max-h-52 divide-y overflow-y-auto rounded-xl border"
        >
          {found.map((item) => {
            const already = allowedIds.has(item.content_id);
            return (
              <li key={item.content_id} className="flex items-center gap-2 px-3 py-2 text-sm">
                <span className="min-w-0 flex-1 truncate">
                  {item.title}
                  <span className="text-muted-foreground ms-1.5 text-xs">
                    {[item.year, t("web.profiles.allowedTitles.resultType", { type: item.type })]
                      .filter(Boolean)
                      .join(" · ")}
                  </span>
                </span>
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  disabled={already}
                  onClick={() => onPick(item)}
                >
                  {already ? (
                    t("web.profiles.allowedTitles.allowed")
                  ) : (
                    <>
                      <Plus className="size-3.5" />
                      {t("web.profiles.allowedTitles.allow")}
                    </>
                  )}
                </Button>
              </li>
            );
          })}
        </ul>
      ) : null}
    </div>
  );
}
