/**
 * Home rows poster peeks: what each surface fetches for the first few titles
 * at the start of a row. Loading them on screen, through the shared budget of
 * requests in flight, is `components/calm/usePeekLimiter`.
 */
import type { ResolvedSection } from "@/api/types";
import type { PreviewItem } from "@/components/calm/usePeekLimiter";
import { previewSection } from "@/lib/recipes";
import { pageParam } from "./pages";
import type { RowDraft } from "./rowDraft";
import { stableJson } from "./stableJson";
import type { HomeRow, PageRef } from "./types";

/** Titles a peek asks for. Phones show the first two. */
export const PEEK_ITEM_LIMIT = 3;

/**
 * The admin preview of a row definition on a page: its first `limit` titles
 * and how many match in all.
 */
export async function fetchRowPreview(
  row: Pick<RowDraft, "sectionType" | "config">,
  page: PageRef,
  limit: number,
  signal?: AbortSignal,
): Promise<{ items: PreviewItem[]; totalCount: number }> {
  const result = await previewSection(
    {
      section_type: row.sectionType,
      config: row.config,
      item_limit: limit,
      ...(page.kind === "library" ? { library_id: page.libraryId } : {}),
    },
    signal,
  );
  return {
    items: result.items.map((item) => ({
      id: item.content_id,
      title: item.title ?? "",
      posterUrl: item.poster_path || undefined,
      thumbhash: item.poster_thumbhash || undefined,
    })),
    totalCount: result.total_count,
  };
}

/**
 * An admin peek previews the row's definition, so the key carries everything
 * the preview reads: the page, the kind and the config. Kept outside
 * `sectionKeys`, which every row write invalidates.
 */
export function adminPeekKey(page: PageRef, row: HomeRow): readonly unknown[] {
  return [
    "home-row-peek",
    "admin",
    pageParam(page),
    row.id,
    row.sectionType,
    stableJson(row.config),
  ];
}

/**
 * A profile peek reads the row as this profile sees it on Home or the library
 * page, so the key carries its kind, config and size as well as the page.
 */
export function profilePeekKey(page: PageRef, row: HomeRow): readonly unknown[] {
  return [
    "home-row-peek",
    "profile",
    pageParam(page),
    row.id,
    row.sectionType,
    stableJson(row.config),
    row.itemLimit,
  ];
}

/** A peek's titles from a row as the viewer's Home resolves it. */
export function peekItemsOf(section: ResolvedSection): PreviewItem[] {
  return section.items.slice(0, PEEK_ITEM_LIMIT).map((item) => ({
    id: item.content_id,
    title: item.title,
    posterUrl: item.poster_url || undefined,
    thumbhash: item.poster_thumbhash || undefined,
  }));
}

/**
 * Titles from Home's own cache of the row, shown while a profile peek loads.
 * The cached row has no config, so it is used only when its kind and size
 * still match and this tab hasn't changed the row since it was cached.
 */
export function profilePeekSeed(
  cached: { data?: { section: ResolvedSection }; dataUpdatedAt: number } | undefined,
  row: HomeRow,
  lastWriteAt: number | undefined,
): PreviewItem[] | undefined {
  const section = cached?.data?.section;
  if (!section || section.section_type !== row.sectionType || section.item_limit !== row.itemLimit)
    return undefined;
  if (lastWriteAt !== undefined && lastWriteAt >= cached.dataUpdatedAt) return undefined;
  return peekItemsOf(section);
}
