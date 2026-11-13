import { useId, useMemo, useState } from "react";
import { useQueries, useQuery } from "@tanstack/react-query";
import { ArrowDown, ArrowUp, Search, X } from "lucide-react";
import type { BrowseItem } from "@/api/types";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { createCatalogSearchState, fetchCatalogPage } from "@/hooks/queries/catalog";
import { fetchWatchDetail } from "@/hooks/queries/items";
import { catalogKeys, itemKeys } from "@/hooks/queries/keys";
import { useDebounce } from "@/hooks/useDebounce";
import { useT, type Translate } from "@/i18n";
import { titleCount } from "@/lib/homeRows/describe";

const SEARCH_LIMIT = 10;
const SEARCH_DEBOUNCE_MS = 250;

function itemLabel(t: Translate, title: string, year?: number): string {
  return year ? t("web.home.addRow.curated.title", { title, year: year }) : title;
}

/**
 * An Editor's Picks row's ordered titles: a catalog search to add titles,
 * then the list in the order the row shows it, with move and remove. Titles
 * saved before the editor opened are named from their detail, showing the
 * raw id until it loads.
 */
export function CuratedTitlesEditor({
  itemIds,
  onChange,
}: {
  itemIds: string[];
  onChange: (itemIds: string[]) => void;
}) {
  const t = useT();
  const listId = useId();
  const [query, setQuery] = useState("");
  const [labels, setLabels] = useState<Record<string, string>>({});
  const debounced = useDebounce(query.trim(), SEARCH_DEBOUNCE_MS);
  const picked = new Set(itemIds);

  const unlabeled = itemIds.filter((id) => !(id in labels));
  const details = useQueries({
    queries: unlabeled.map((id) => ({
      queryKey: itemKeys.watchDetail(id),
      queryFn: ({ signal }: { signal?: AbortSignal }) =>
        fetchWatchDetail(id, undefined, undefined, { signal }),
      staleTime: 5 * 60 * 1000,
      retry: false,
    })),
  });
  const loadedLabels: Record<string, string> = {};
  unlabeled.forEach((id, index) => {
    const detail = details[index]?.data;
    if (detail) loadedLabels[id] = itemLabel(t, detail.title, detail.year);
  });

  const searchState = useMemo(
    () => createCatalogSearchState("query", { q: debounced || undefined }),
    [debounced],
  );
  const results = useQuery({
    queryKey: [
      "curatedListPicker",
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
  const found: BrowseItem[] = results.data?.items ?? [];

  function add(item: BrowseItem) {
    if (picked.has(item.content_id)) return;
    setLabels((current) => ({
      ...current,
      [item.content_id]: itemLabel(t, item.title, item.year),
    }));
    onChange([...itemIds, item.content_id]);
  }

  function move(index: number, delta: number) {
    const target = index + delta;
    if (target < 0 || target >= itemIds.length) return;
    const next = [...itemIds];
    const [id] = next.splice(index, 1);
    next.splice(target, 0, id!);
    onChange(next);
  }

  const nameOf = (id: string) => labels[id] ?? loadedLabels[id] ?? id;

  let searchStatus: string | null = null;
  if (debounced.length > 0) {
    if (results.isLoading) searchStatus = t("web.home.addRow.curated.searching");
    else if (results.isError) searchStatus = t("web.home.addRow.curated.searchFailed");
    else if (found.length === 0) searchStatus = t("web.home.addRow.curated.noMatch");
  }

  return (
    <div className="grid gap-3">
      <div className="grid gap-2">
        <span className="text-sm font-medium">{t("web.home.addRow.curated.titles")}</span>
        <div className="relative">
          <Search
            aria-hidden
            className="text-muted-foreground absolute start-3 top-1/2 size-4 -translate-y-1/2"
          />
          <Input
            type="search"
            aria-label={t("web.home.addRow.curated.search")}
            placeholder={t("web.home.addRow.curated.searchPlaceholder")}
            className="ps-9"
            autoComplete="off"
            value={query}
            onChange={(event) => setQuery(event.target.value)}
          />
        </div>
        {searchStatus ? (
          <p role="status" className="text-muted-foreground text-[13px]">
            {searchStatus}
          </p>
        ) : debounced.length > 0 ? (
          <ul
            aria-label={t("web.home.addRow.curated.results")}
            className="border-border divide-border max-h-52 divide-y overflow-y-auto rounded-xl border"
          >
            {found.map((item) => {
              const already = picked.has(item.content_id);
              return (
                <li key={item.content_id} className="flex items-center gap-2 px-3 py-2 text-sm">
                  <span className="min-w-0 flex-1 truncate">
                    {item.title}
                    <span className="text-muted-foreground ms-1.5 text-xs">
                      {item.year
                        ? t("web.home.addRow.curated.resultMeta", {
                            year: item.year,
                            type: item.type,
                          })
                        : item.type}
                    </span>
                  </span>
                  <Button
                    type="button"
                    size="sm"
                    variant="outline"
                    disabled={already}
                    aria-label={
                      already
                        ? t("web.home.addRow.curated.isAdded", { title: item.title })
                        : t("web.home.addRow.curated.addTitle", { title: item.title })
                    }
                    onClick={() => add(item)}
                  >
                    {already
                      ? t("web.home.addRow.curated.added")
                      : t("web.home.addRow.curated.add")}
                  </Button>
                </li>
              );
            })}
          </ul>
        ) : null}
      </div>

      <div className="grid gap-2">
        <span id={listId} className="text-muted-foreground text-[13px]">
          {t("web.home.addRow.curated.inOrder", { titles: titleCount(itemIds.length) })}
        </span>
        {itemIds.length === 0 ? (
          <p className="border-border text-muted-foreground rounded-xl border border-dashed px-3 py-3 text-[13px]">
            {t("web.home.addRow.curated.empty")}
          </p>
        ) : (
          <ol
            aria-labelledby={listId}
            className="border-border divide-border divide-y rounded-xl border"
          >
            {itemIds.map((id, index) => (
              <li key={id} className="flex items-center gap-1 py-1.5 ps-3 pe-1.5 text-sm">
                <span className="min-w-0 flex-1 truncate">{nameOf(id)}</span>
                <Button
                  type="button"
                  size="icon"
                  variant="ghost"
                  className="size-8"
                  aria-label={t("web.home.addRow.curated.moveUp", { title: nameOf(id) })}
                  disabled={index === 0}
                  onClick={() => move(index, -1)}
                >
                  <ArrowUp aria-hidden className="size-4" />
                </Button>
                <Button
                  type="button"
                  size="icon"
                  variant="ghost"
                  className="size-8"
                  aria-label={t("web.home.addRow.curated.moveDown", { title: nameOf(id) })}
                  disabled={index === itemIds.length - 1}
                  onClick={() => move(index, 1)}
                >
                  <ArrowDown aria-hidden className="size-4" />
                </Button>
                <Button
                  type="button"
                  size="icon"
                  variant="ghost"
                  className="text-muted-foreground hover:text-destructive size-8"
                  aria-label={t("web.home.addRow.curated.remove", { title: nameOf(id) })}
                  onClick={() => onChange(itemIds.filter((existing) => existing !== id))}
                >
                  <X aria-hidden className="size-4" />
                </Button>
              </li>
            ))}
          </ol>
        )}
      </div>
    </div>
  );
}
