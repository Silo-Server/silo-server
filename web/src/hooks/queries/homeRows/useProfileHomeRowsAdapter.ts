import { useCallback, useEffect, useMemo } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useSearchParams } from "react-router";
import type { HomeSectionItemsResponse, SettingsSectionEntry } from "@/api/types";
import { sectionKeys } from "@/hooks/queries/keys";
import { useUserLibraries } from "@/hooks/queries/libraries";
import { fetchHomeSectionItems, fetchLibrarySectionItems } from "@/hooks/queries/sections";
import { pageParam, parsePageParam, samePage } from "@/lib/homeRows/pages";
import {
  buildProfileRowCreate,
  buildProfileRowUpdate,
  nextAppendPosition,
} from "@/lib/homeRows/payloads";
import type { PeekRequest } from "@/components/calm/usePeekLimiter";
import { peekItemsOf, profilePeekKey, profilePeekSeed } from "@/lib/homeRows/peek";
import { draftFromRow, mergeReloadedDraft, type RowDraft } from "@/lib/homeRows/rowDraft";
import type {
  EditSession,
  HomeRow,
  HomeRowsAdapter,
  HomeRowsPageOption,
  PageRef,
} from "@/lib/homeRows/types";
import { fetchRecipeCatalog, type RecipeCatalogResponse } from "@/lib/recipes";
import { isTraktConfig } from "@/lib/sectionTypes";
import { useProfileHomeRows } from "./useProfileHomeRows";
import { useProfileRowCollections } from "./useRowCollectionOptions";

const FIVE_MINUTES = 5 * 60 * 1000;

export interface ProfileHomeRowsAdapter extends HomeRowsAdapter {
  /** Removes server rows from this page and deletes the profile's own rows, in one save. */
  remove(ids: string[]): void;
  /** Drops every change this profile made on this page. */
  reset(): void;
  /** Gives a renamed server row its server name back, and lets it follow later renames. */
  restoreOriginalName(id: string): void;
  /** When a save holding this row last went through, if one has. */
  lastWriteAt(rowId: string): number | undefined;
  /** The saved changes failed to load, so editing stays off. */
  overridesFailed: boolean;
  /** The rows are on screen but the saved changes are still loading, so editing waits. */
  overridesLoading: boolean;
  catalog: RecipeCatalogResponse | undefined;
  catalogFailed: boolean;
}

function toHomeRow(section: SettingsSectionEntry): HomeRow {
  const renamed =
    !section.is_custom && section.default_title && section.title !== section.default_title;
  return {
    id: section.id,
    title: section.title,
    sectionType: section.section_type,
    config: section.config ?? {},
    itemLimit: section.item_limit,
    hero: section.featured,
    shown: !section.hidden,
    own: section.is_custom,
    legacyTrakt: isTraktConfig(section.config),
    renamedFrom: renamed ? section.default_title : undefined,
  };
}

function viewerItemsKey(page: PageRef, rowId: string) {
  return page.kind === "home"
    ? sectionKeys.homeItems(rowId)
    : sectionKeys.libraryItems(page.libraryId, rowId);
}

/**
 * Settings > Home Screen as a Home rows adapter: this profile's rows on one
 * page (`?page=home|<libraryId>`), saved through `useProfileHomeRows`. Every
 * profile may add rule rows; no server setting is read.
 */
export function useProfileHomeRowsAdapter(): ProfileHomeRowsAdapter {
  const queryClient = useQueryClient();
  const homeRows = useProfileHomeRows();
  const librariesQuery = useUserLibraries();
  const libraries = librariesQuery.data;
  const [searchParams, setSearchParams] = useSearchParams();
  const rawPage = searchParams.get("page");
  // A library link can only be checked once the libraries load.
  const pageKnown =
    libraries !== undefined || librariesQuery.isError || rawPage === null || rawPage === "home";
  const requested = useMemo(
    () =>
      parsePageParam(
        rawPage,
        (libraries ?? []).map((library) => library.id),
      ),
    [rawPage, libraries],
  );
  const { page, setPage: setHookPage, pending } = homeRows;
  const onRequestedPage = samePage(page, requested);

  // Follows the address, including a link that opens a library page. A switch
  // is refused while this page still has saves to send, and retried after.
  useEffect(() => {
    if (pageKnown && !onRequestedPage) setHookPage(requested);
  }, [pageKnown, onRequestedPage, requested, pending, setHookPage]);

  const setPage = useCallback(
    (ref: PageRef) => {
      if (!setHookPage(ref)) return;
      setSearchParams(
        (current) => {
          const next = new URLSearchParams(current);
          next.set("page", pageParam(ref));
          return next;
        },
        { replace: true },
      );
    },
    [setHookPage, setSearchParams],
  );

  const pages = useMemo<HomeRowsPageOption[]>(
    () => [
      { ref: { kind: "home" }, label: "Home" },
      ...(libraries ?? []).map((library) => ({
        ref: { kind: "library" as const, libraryId: library.id },
        label: library.name,
      })),
    ],
    [libraries],
  );

  const catalogQuery = useQuery({
    queryKey: ["recipe-catalog"],
    queryFn: fetchRecipeCatalog,
    staleTime: FIVE_MINUTES,
  });
  const catalog = catalogQuery.data;
  const collections = useProfileRowCollections();

  const { sections } = homeRows;
  const rows = useMemo(() => sections.map(toHomeRow), [sections]);
  const { canEdit } = homeRows;

  let status: HomeRowsAdapter["status"] = "ready";
  if (!pageKnown || !onRequestedPage) status = "loading";
  else if (homeRows.loadError) status = "error";
  else if (homeRows.loading) status = "loading";

  const sectionFor = useCallback(
    (id: string) => sections.find((section) => section.id === id),
    [sections],
  );

  const assertEditable = useCallback(() => {
    if (!canEdit) throw new Error("This page can't change right now.");
  }, [canEdit]);

  const { saveSection, setHidden, move, lastWriteAt } = homeRows;

  const create = useCallback(
    async (draft: RowDraft) => {
      assertEditable();
      const entry = buildProfileRowCreate(
        draft,
        draft.title,
        nextAppendPosition(sections.map((section) => section.position)),
      );
      saveSection(entry);
      return { newIds: [entry.id] };
    },
    [assertEditable, saveSection, sections],
  );

  const openEdit = useCallback(
    async (id: string): Promise<EditSession> => {
      const section = sectionFor(id);
      if (!section) throw new Error("This row is no longer on this page.");
      // A profile changes how a server row looks, never what it shows.
      return { row: toHomeRow(section), token: section, kindLocked: !section.is_custom };
    },
    [sectionFor],
  );

  const save = useCallback(
    async (session: EditSession, draft: RowDraft) => {
      assertEditable();
      // Built on the row as it is now: fields the user left as the dialog
      // opened them take the row's current values, so a switch, hero or
      // server change made meanwhile stays and isn't pinned.
      const current = sectionFor(session.row.id);
      const section = current ?? (session.token as SettingsSectionEntry);
      const merged = current
        ? mergeReloadedDraft(
            draftFromRow(session.row, catalog),
            draft,
            draftFromRow(toHomeRow(current), catalog),
          ).draft
        : draft;
      saveSection(buildProfileRowUpdate(section, merged, merged.title));
    },
    [assertEditable, catalog, saveSection, sectionFor],
  );

  const setHero = useCallback(
    async (id: string, hero: boolean) => {
      const section = sectionFor(id);
      if (section && canEdit) saveSection({ ...section, featured: hero });
    },
    [canEdit, saveSection, sectionFor],
  );

  const restoreOriginalName = useCallback(
    (id: string) => {
      const section = sectionFor(id);
      if (section?.default_title && canEdit)
        saveSection({ ...section, title: section.default_title });
    },
    [canEdit, saveSection, sectionFor],
  );

  const reorder = useCallback(
    async (orderedIds: string[], _orderToken?: unknown, movedId?: string) => {
      // A single move, placed by the order captured when the drag started.
      if (canEdit && movedId) move(movedId, orderedIds);
    },
    [canEdit, move],
  );

  // A peek shows the row as this profile's Home or library page resolves it,
  // from the row as last saved: a change still on its way would otherwise be
  // fetched before it lands and cached under its new key.
  const { savedSections } = homeRows;
  const serverSections = useMemo(
    () => new Map(savedSections.map((section) => [section.id, section])),
    [savedSections],
  );
  const peek = useCallback(
    (row: HomeRow): PeekRequest | null => {
      const saved = serverSections.get(row.id);
      if (!saved || saved.hidden) return null;
      const savedRow = toHomeRow(saved);
      return {
        queryKey: profilePeekKey(page, savedRow),
        fetch: async (signal) => {
          const response =
            page.kind === "home"
              ? await fetchHomeSectionItems(savedRow.id, { signal })
              : await fetchLibrarySectionItems(page.libraryId, savedRow.id, { signal });
          return peekItemsOf(response.section);
        },
        placeholder: () =>
          profilePeekSeed(
            queryClient.getQueryState<HomeSectionItemsResponse>(viewerItemsKey(page, savedRow.id)),
            savedRow,
            lastWriteAt(savedRow.id),
          ),
      };
    },
    [lastWriteAt, page, queryClient, serverSections],
  );

  return {
    surface: "profile",
    page,
    pages,
    setPage,
    status,
    error: homeRows.loadError?.message ?? null,
    canEdit,
    rows,
    pending,
    conflict: null,
    reload: homeRows.reload,
    canReorder: canEdit,
    orderToken: undefined,
    reorder,
    setShown: async (id, shown) => {
      if (canEdit) setHidden(id, !shown);
    },
    setHero,
    capabilities: { draftPreview: false, libraryCopies: false },
    create,
    openEdit,
    reloadEdit: (session) => openEdit(session.row.id),
    save,
    peek,
    collections,
    remove: homeRows.remove,
    reset: homeRows.reset,
    restoreOriginalName,
    lastWriteAt,
    overridesFailed: homeRows.overridesFailed,
    overridesLoading: status === "ready" && !homeRows.ready && !homeRows.overridesFailed,
    catalog,
    catalogFailed: catalogQuery.isError,
  };
}
