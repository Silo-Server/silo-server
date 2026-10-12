import { useCallback, useMemo, useRef, useState } from "react";
import { useIsMutating, useQueryClient } from "@tanstack/react-query";
import { useSearchParams } from "react-router";
import { toast } from "sonner";
import {
  adminSectionMutationMessage,
  bulkCreateAdminSections,
  createAdminSection,
  fetchAdminSectionSnapshot,
  reorderAdminSections,
  updateAdminSection,
  type fetchAdminSections,
} from "@/api/adminSections";
import type { PageSectionConfig } from "@/api/types";
import { V2ProblemError } from "@/api/v2/request";
import { useAdminLibraries } from "@/hooks/queries/admin/libraries";
import { sectionKeys } from "@/hooks/queries/keys";
import { useAdminSectionCapabilities, useAdminSections } from "@/hooks/queries/sections";
import { useAdminRowCollections } from "./useRowCollectionOptions";
import { libraryPagesOf, pageParam, parsePageParam, samePage } from "@/lib/homeRows/pages";
import type { PeekRequest } from "@/components/calm/usePeekLimiter";
import { adminPeekKey, fetchRowPreview, PEEK_ITEM_LIMIT } from "@/lib/homeRows/peek";
import { canCopyToLibraries, libraryCopyIds } from "@/lib/homeRows/bulkCopy";
import {
  buildBulkCopyPayload,
  buildRowCreateRequest,
  buildRowUpdateRequest,
  nextAppendPosition,
} from "@/lib/homeRows/payloads";
import type { RowDraft } from "@/lib/homeRows/rowDraft";
import { stableJson } from "@/lib/homeRows/stableJson";
import {
  RowChangedError,
  type EditSession,
  type HomeRow,
  type HomeRowsAdapter,
  type HomeRowsConflict,
  type HomeRowsPageOption,
  type PageRef,
} from "@/lib/homeRows/types";
import { isTraktConfig } from "@/lib/sectionTypes";

type AdminSectionList = Awaited<ReturnType<typeof fetchAdminSections>>;
type QuickField = "shown" | "hero";

/** Lists stay under 10,000 rows; past that a full-order PUT is refused anyway. */
const MAX_REORDER_ROWS = 10000;

/** Rows a select-mode batch reads and writes at once. */
const BATCH_PARALLEL = 4;

/** Why a row in a select-mode batch was left as it was. */
export interface BatchFailure {
  id: string;
  title: string;
  /** changed: it no longer matches the page; legacy: a Trakt row can't be turned back on. */
  reason: "changed" | "legacy" | "failed";
  message?: string;
}

export interface ShownBatchResult {
  changedIds: string[];
  failures: BatchFailure[];
}

function toHomeRow(section: PageSectionConfig): HomeRow {
  return {
    id: section.id,
    title: section.title,
    sectionType: section.section_type,
    config: section.config ?? {},
    itemLimit: section.item_limit,
    hero: section.featured,
    shown: section.enabled,
    own: false,
    legacyTrakt: isTraktConfig(section.config),
  };
}

/** Whether the row the server holds now is the row this page is showing. */
function sameRow(a: PageSectionConfig, b: PageSectionConfig): boolean {
  return (
    a.title === b.title &&
    a.section_type === b.section_type &&
    a.enabled === b.enabled &&
    a.featured === b.featured &&
    a.item_limit === b.item_limit &&
    stableJson(a.config) === stableJson(b.config)
  );
}

function orderRows(sections: PageSectionConfig[], draft: string[] | null): PageSectionConfig[] {
  if (!draft) return sections;
  const byId = new Map(sections.map((section) => [section.id, section]));
  const ordered = draft.flatMap((id) => byId.get(id) ?? []);
  const placed = new Set(draft);
  return [...ordered, ...sections.filter((section) => !placed.has(section.id))];
}

/** What an admin Edit row session keeps: the stored row and the version it was read at. */
interface AdminEditToken {
  section: PageSectionConfig;
  etag: string;
}

function adminSession(section: PageSectionConfig, etag: string): EditSession {
  return { row: toHomeRow(section), token: { section, etag } satisfies AdminEditToken };
}

/** The row or page moved on (412), or the row is gone (404). */
function isStale(error: unknown) {
  return error instanceof V2ProblemError && (error.status === 412 || error.status === 404);
}

export interface AdminHomeRows extends HomeRowsAdapter {
  scope: "home" | "library";
  libraryId: number | undefined;
  /** The page's rows as the server sent them, for the edit and delete flows. */
  sections: PageSectionConfig[];
  serverCapabilities: ReturnType<typeof useAdminSectionCapabilities>["data"];
  /**
   * Turns several rows on or off: each row is read and checked against the
   * page like a single switch, four at a time, then the list refetches once.
   * Rows already in that state are still read, then skipped.
   */
  setShownMany(ids: string[], shown: boolean): Promise<ShownBatchResult>;
  /**
   * Adds a copy of an existing row to each of `libraryIds` other than this
   * page. Rejects with RowChangedError when the row no longer matches the page.
   */
  copyToLibraries(id: string, libraryIds: number[]): Promise<void>;
}

/**
 * The admin Home rows adapter. Reads one page's rows and runs every list write
 * (switch, hero, reorder) one at a time. `pending` stays set while any admin
 * row write is in flight and until the refetch after it lands, because any row
 * write bumps the page version a reorder is checked against. Quick actions
 * first read the row and refuse to write when it no longer matches what the
 * page shows.
 */
export function useAdminHomeRows(): AdminHomeRows {
  const queryClient = useQueryClient();
  const [searchParams, setSearchParams] = useSearchParams();
  const librariesQuery = useAdminLibraries();
  const librariesData = librariesQuery.data;
  const libraries = useMemo(() => librariesData ?? [], [librariesData]);
  const rawPage = searchParams.get("page");
  // A library link can only be checked once the libraries load; until then the
  // page waits instead of showing Home for a moment. If they fail to load, the
  // page reports that and Reload retries them.
  const pageKnown = librariesData !== undefined || rawPage === null || rawPage === "home";
  const librariesFailed = !pageKnown && librariesQuery.isError;
  const page = parsePageParam(
    rawPage,
    libraries.map((library) => library.id),
  );
  const scope = page.kind;
  const libraryId = page.kind === "library" ? page.libraryId : undefined;
  const pages = useMemo<HomeRowsPageOption[]>(
    () => [
      { ref: { kind: "home" }, label: "Home" },
      ...libraries.map((library) => ({
        ref: { kind: "library" as const, libraryId: library.id },
        label: library.name,
        libraryType: library.type,
      })),
    ],
    [libraries],
  );

  const list = useAdminSections(scope, libraryId, pageKnown);
  const collections = useAdminRowCollections();
  const { data: capabilities } = useAdminSectionCapabilities();
  const [draft, setDraft] = useState<string[] | null>(null);
  const [conflict, setConflict] = useState<HomeRowsConflict>(null);
  const [optimistic, setOptimistic] = useState<
    Record<string, Partial<Record<QuickField, boolean>>>
  >({});
  // The newest quick action per row and field. Only that action clears the
  // field's on-screen value, so an earlier toggle that finishes first cannot
  // flash the row back to the server value.
  const latestQuickAction = useRef(new Map<string, number>());
  const quickActionCount = useRef(0);
  const [pendingCount, setPendingCount] = useState(0);
  const queue = useRef<Promise<void>>(Promise.resolve());
  // Writes made outside this hook (add, edit, delete, restore) and the refetch
  // after them hold the list too: until the new version arrives, a reorder would
  // be checked against the old one and fail.
  const otherWrites = useIsMutating({ mutationKey: sectionKeys.adminWrite() });
  const refetching = list.isFetching && list.data !== undefined;
  const pending = pendingCount > 0 || otherWrites > 0 || refetching;

  const sections = useMemo(() => list.data?.sections ?? [], [list.data?.sections]);
  const rows = useMemo(
    () =>
      orderRows(sections, draft).map((section) => {
        const override = optimistic[section.id];
        const mapped = toHomeRow(section);
        return override ? { ...mapped, ...override } : mapped;
      }),
    [sections, draft, optimistic],
  );

  let status: HomeRowsAdapter["status"] = "ready";
  if (librariesFailed || (list.isError && !list.data)) status = "error";
  else if (!pageKnown || list.isLoading) status = "loading";
  const readError = librariesFailed ? librariesQuery.error : list.error;
  // A refetch that fails after a good read (for example "Sections changed while
  // loading") keeps the old rows on screen but marks them out of date.
  const effectiveConflict: HomeRowsConflict =
    conflict ?? (list.isError && list.data ? { scope: "page" } : null);
  const canEdit = status === "ready" && !list.isError && Boolean(capabilities?.available);
  const canReorder =
    canEdit &&
    !pending &&
    effectiveConflict === null &&
    draft === null &&
    Boolean(list.data?.etag) &&
    rows.length <= MAX_REORDER_ROWS;

  const enqueue = useCallback(<T>(job: () => Promise<T>): Promise<T> => {
    setPendingCount((count) => count + 1);
    const run = queue.current.then(job).finally(() => setPendingCount((count) => count - 1));
    queue.current = run.then(
      () => undefined,
      () => undefined,
    );
    return run;
  }, []);

  const refresh = useCallback(
    () => queryClient.invalidateQueries({ queryKey: sectionKeys.all }),
    [queryClient],
  );

  const currentList = useCallback(
    () =>
      queryClient.getQueryData<AdminSectionList>(sectionKeys.adminList(scope, libraryId))
        ?.sections ?? [],
    [queryClient, scope, libraryId],
  );

  const quickAction = useCallback(
    (id: string, field: QuickField, value: boolean) => {
      const key = `${id}:${field}`;
      const sequence = ++quickActionCount.current;
      latestQuickAction.current.set(key, sequence);
      const clearOptimistic = () => {
        if (latestQuickAction.current.get(key) !== sequence) return;
        latestQuickAction.current.delete(key);
        setOptimistic((current) => {
          const { [field]: _dropped, ...rest } = current[id] ?? {};
          const next = { ...current };
          if (Object.keys(rest).length > 0) next[id] = rest;
          else delete next[id];
          return next;
        });
      };
      setOptimistic((current) => ({ ...current, [id]: { ...current[id], [field]: value } }));
      return enqueue(async () => {
        try {
          const onScreen = currentList().find((section) => section.id === id);
          const snapshot = await fetchAdminSectionSnapshot(id);
          if (!onScreen || !sameRow(snapshot.section, onScreen)) {
            setConflict({ scope: "row", rowId: id });
            return;
          }
          await updateAdminSection({
            id,
            etag: snapshot.etag,
            ...(field === "shown" ? { enabled: value } : { featured: value }),
          });
          await refresh();
        } catch (error) {
          if (isStale(error)) setConflict({ scope: "row", rowId: id });
          else toast.error(adminSectionMutationMessage(error, "Could not save this row"));
          // Without a server answer the write may still have landed, so read
          // the page again before showing the row's value.
          if (!(error instanceof V2ProblemError)) await refresh();
        } finally {
          clearOptimistic();
        }
      });
    },
    [currentList, enqueue, refresh],
  );

  const setShownMany = useCallback(
    (ids: string[], shown: boolean) =>
      enqueue(async (): Promise<ShownBatchResult> => {
        const onScreen = new Map(currentList().map((section) => [section.id, section]));
        const changedIds: string[] = [];
        const failures: BatchFailure[] = [];
        // Returns why the row was left as it was, or null when it changed or needed no change.
        const writeOne = async (id: string): Promise<BatchFailure["reason"] | null> => {
          const section = onScreen.get(id);
          if (!section) return "changed";
          if (shown && !section.enabled && isTraktConfig(section.config)) return "legacy";
          // Read even a row that already looks right: another admin may have
          // flipped it since this page loaded.
          const snapshot = await fetchAdminSectionSnapshot(id);
          if (!sameRow(snapshot.section, section)) return "changed";
          if (section.enabled === shown) return null;
          await updateAdminSection({ id, etag: snapshot.etag, enabled: shown });
          changedIds.push(id);
          return null;
        };
        const settle = async (id: string) => {
          const title = onScreen.get(id)?.title ?? id;
          try {
            const reason = await writeOne(id);
            if (reason) failures.push({ id, title, reason });
          } catch (error) {
            failures.push(
              isStale(error)
                ? { id, title, reason: "changed" }
                : {
                    id,
                    title,
                    reason: "failed",
                    message: adminSectionMutationMessage(error, "Could not save this row"),
                  },
            );
          }
        };
        for (let start = 0; start < ids.length; start += BATCH_PARALLEL) {
          await Promise.all(ids.slice(start, start + BATCH_PARALLEL).map(settle));
        }
        // One refetch for the whole batch: until it lands the page version is stale.
        await refresh();
        return { changedIds, failures };
      }),
    [currentList, enqueue, refresh],
  );

  const reorder = useCallback(
    (orderedIds: string[], orderToken?: unknown) => {
      const etag = typeof orderToken === "string" ? orderToken : (list.data?.etag ?? "");
      setDraft(orderedIds);
      return enqueue(async () => {
        try {
          await reorderAdminSections({
            scope,
            library_id: libraryId,
            etag,
            ordered_ids: orderedIds,
          });
          await refresh();
          setDraft(null);
        } catch (error) {
          if (isStale(error)) {
            setConflict({ scope: "page" });
          } else {
            toast.error(adminSectionMutationMessage(error, "Could not move this row"));
            // Without a server answer the order may still have changed, so read
            // the page again before dropping the order on screen.
            if (!(error instanceof V2ProblemError)) await refresh();
            setDraft(null);
          }
        }
      });
    },
    [enqueue, libraryId, list.data?.etag, refresh, scope],
  );

  const create = useCallback(
    (draft: RowDraft) =>
      enqueue(async () => {
        const copies = libraryCopyIds(draft, page, libraryPagesOf(pages));
        if (page.kind === "library" && copies.length > 0) {
          // One transaction puts the row at the bottom of each page. It returns
          // no ids, so the new row here is the one the refetch adds.
          const before = new Set(currentList().map((section) => section.id));
          await bulkCreateAdminSections(
            buildBulkCopyPayload({ ...draft, enabled: true }, [page.libraryId, ...copies]),
          );
          await refresh();
          return {
            newIds: currentList()
              .map((section) => section.id)
              .filter((id) => !before.has(id)),
          };
        }
        // Read the position when the write runs, after any write queued before it.
        const position = nextAppendPosition(currentList().map((section) => section.position));
        const created = await createAdminSection(
          buildRowCreateRequest(draft, draft.title, page, position),
        );
        await refresh();
        return { newIds: [created.id] };
      }),
    [currentList, enqueue, page, pages, refresh],
  );

  const copyToLibraries = useCallback(
    (id: string, libraryIds: number[]) =>
      enqueue(async () => {
        const targets = [...new Set(libraryIds)].filter((target) => target !== libraryId);
        if (targets.length === 0) return;
        // Copies are made from the row the server holds, and only when it is
        // still the row on screen, like a quick action.
        const onScreen = currentList().find((section) => section.id === id);
        const { section } = await fetchAdminSectionSnapshot(id);
        if (!onScreen || !sameRow(section, onScreen)) {
          setConflict({ scope: "row", rowId: id });
          throw new RowChangedError();
        }
        const source = toHomeRow(section);
        if (!canCopyToLibraries(source))
          throw new Error("This row can't be added to other libraries.");
        await bulkCreateAdminSections(
          buildBulkCopyPayload({ ...source, enabled: section.enabled }, targets),
        );
        await refresh();
      }),
    [currentList, enqueue, libraryId, refresh],
  );

  const openEdit = useCallback(async (id: string) => {
    const snapshot = await fetchAdminSectionSnapshot(id);
    return adminSession(snapshot.section, snapshot.etag);
  }, []);

  const reloadEdit = useCallback(
    (session: EditSession) => openEdit((session.token as AdminEditToken).section.id),
    [openEdit],
  );

  const save = useCallback(
    (session: EditSession, draft: RowDraft) =>
      enqueue(async () => {
        const { section, etag } = session.token as AdminEditToken;
        try {
          await updateAdminSection({
            ...buildRowUpdateRequest(section, draft, draft.title),
            id: section.id,
            etag,
          });
        } catch (error) {
          if (error instanceof V2ProblemError && error.status === 412) throw new RowChangedError();
          throw error;
        }
        await refresh();
      }),
    [enqueue, refresh],
  );

  // Peeks preview the row's own definition, not the admin's profile view of it,
  // so a profile override never shows here as the server row.
  const previewAvailable = Boolean(capabilities?.preview);
  const peek = useCallback(
    (row: HomeRow): PeekRequest | null =>
      previewAvailable
        ? {
            queryKey: adminPeekKey(page, row),
            fetch: async (signal) =>
              (await fetchRowPreview(row, page, PEEK_ITEM_LIMIT, signal)).items,
          }
        : null,
    [page, previewAvailable],
  );

  const reload = useCallback(async () => {
    if (librariesFailed) {
      await librariesQuery.refetch();
      return;
    }
    const result = await list.refetch();
    if (!result.isError) {
      setConflict(null);
      setDraft(null);
    }
  }, [librariesFailed, librariesQuery, list]);

  const setPage = useCallback(
    (ref: PageRef) => {
      if (pending || samePage(ref, page)) return;
      setDraft(null);
      setConflict(null);
      setOptimistic({});
      setSearchParams(
        (current) => {
          const next = new URLSearchParams(current);
          next.set("page", pageParam(ref));
          return next;
        },
        { replace: true },
      );
    },
    [page, pending, setSearchParams],
  );

  return {
    surface: "admin",
    page,
    pages,
    setPage,
    status,
    error: readError instanceof Error ? readError.message : null,
    canEdit,
    rows,
    pending,
    conflict: effectiveConflict,
    reload,
    canReorder,
    orderToken: list.data?.etag,
    reorder,
    setShown: (id, shown) => quickAction(id, "shown", shown),
    setHero: (id, hero) => quickAction(id, "hero", hero),
    capabilities: {
      draftPreview: previewAvailable,
      libraryCopies: page.kind === "library",
    },
    create,
    copyToLibraries,
    openEdit,
    reloadEdit,
    save,
    peek,
    collections,
    scope,
    libraryId,
    sections,
    serverCapabilities: capabilities,
    setShownMany,
  };
}
