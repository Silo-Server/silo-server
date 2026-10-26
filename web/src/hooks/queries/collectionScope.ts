import { useCallback, useMemo, useRef, useState } from "react";
import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";

import { normalizeQueryDefinition, type QueryDefinition } from "@/api/types";
import { isNotFoundProblem, v2, V2ProblemError } from "@/api/v2/request";
import { useDebounce } from "@/hooks/useDebounce";
import { ARTWORK_SLOT_LABEL, DRAFT_FIELD_LABEL, SAVE_FAILED } from "@/lib/collections/copy";
import { changedFields, mergeDraft, takeFields, type DraftField } from "@/lib/collections/draft";
import type {
  ArtworkDraft,
  ArtworkSlot,
  ArtworkSlotDraft,
  CollectionDraft,
  CollectionScope,
  CollectionView,
  CreatableDraft,
  CreateKind,
  EditorSnapshot,
  PreviewItem,
  SavableDraft,
  SyncOutcome,
  WireCollection,
} from "@/lib/collections/scope";

/**
 * An editor read may carry no artwork URLs (they are presigned, and would move
 * its ETag), so a collection's artwork comes from the scope's list, with
 * whether its poster is the server's collage.
 */
function withListedArtwork<Raw extends WireCollection>(fetched: Raw, listed: Raw | undefined): Raw {
  if (!listed) return fetched;
  const poster = {
    poster_url: listed.poster_url ?? fetched.poster_url,
    poster_thumbhash: listed.poster_thumbhash ?? fetched.poster_thumbhash,
    poster_is_collage: listed.poster_is_collage,
  };
  return "backdrop_url" in fetched && "backdrop_url" in listed
    ? { ...fetched, ...poster, backdrop_url: listed.backdrop_url }
    : { ...fetched, ...poster };
}

/** How long, and how often, an editor reads the list again for a collage being made. */
const COLLAGE_WAIT_MS = 15_000;
const COLLAGE_POLL_MS = 3_000;

/**
 * The poster a collection shows now, as its scope's list has it, falling
 * back to the editor's copy. A collection with no poster of its own shows the
 * collage the server makes of its titles in the background after a change, so
 * while one with titles shows no poster (`awaitCollage`), the list is read
 * again every few seconds for a short while. `version` names the saved state
 * the editor holds (its ETag): a save moves it and starts a new wait, even
 * after an earlier one gave up.
 */
export function useListedPoster<Raw extends WireCollection>(
  scope: CollectionScope<Raw>,
  view: CollectionView<Raw> | undefined,
  { awaitCollage, version }: { awaitCollage: boolean; version?: string },
): { url?: string; isCollage: boolean } {
  const id = view?.id;
  // When this wait for a collage began, and for which saved version; reset
  // once the poster arrives.
  const waiting = useRef<{ since: number; version?: string } | null>(null);
  const { data: listed } = useQuery({
    queryKey: scope.keys.list,
    queryFn: () => scope.fetchList(),
    enabled: Boolean(id),
    select: (data) => data.collections.find((entry) => entry.id === id),
    refetchInterval: (query) => {
      const entry = query.state.data?.collections.find((c) => c.id === id);
      if (!awaitCollage || !entry || entry.poster_url || !entry.item_count) {
        waiting.current = null;
        return false;
      }
      let wait = waiting.current;
      if (!wait || wait.version !== version) {
        wait = { since: Date.now(), version };
        waiting.current = wait;
      }
      return Date.now() - wait.since < COLLAGE_WAIT_MS ? COLLAGE_POLL_MS : false;
    },
  });
  const shown = useMemo(() => (listed ? scope.toView(listed) : view), [listed, scope, view]);
  return { url: shown?.posterUrl, isCollage: shown?.posterIsCollage ?? false };
}

/**
 * The collection an editor opens, with the ETag its next save must send.
 *
 * The editor reads the collection when it opens, even when the collection's
 * page cached a copy, so it never starts from a version whose save can only
 * answer 412. That read is kept for the life of the page, so a background
 * refetch never replaces what someone is editing. It carries the list's
 * artwork when the list is there; a scope with `editorAwaitsList` waits for
 * the list first. A 404 outranks the kept copy: the collection is gone.
 */
export function useScopeEditor<Raw extends WireCollection>(
  scope: CollectionScope<Raw>,
  id: string | undefined,
) {
  const list = useQuery({
    queryKey: scope.keys.list,
    queryFn: () => scope.fetchList(),
    select: (data) => data.collections,
  });
  const fetched = useScopeSnapshot(scope, id, { refetchOnMount: "always" });
  const [frozen, setFrozen] = useState<EditorSnapshot<Raw>>();
  const awaitingList = scope.editorAwaitsList && list.isLoading;
  const awaitingRead = Boolean(id) && !fetched.isFetchedAfterMount;
  if (
    fetched.data &&
    !awaitingList &&
    !awaitingRead &&
    fetched.data.view.id === id &&
    frozen?.view.id !== id
  ) {
    const listed = list.data?.find((entry) => entry.id === id);
    setFrozen({
      ...fetched.data,
      view: scope.toView(withListedArtwork(fetched.data.view.raw, listed)),
    });
  }
  const gone = isNotFoundProblem(fetched.error);
  return {
    snapshot: id && frozen?.view.id === id && !gone ? frozen : undefined,
    /** Still reading what the editor needs: the collection with an id, or the list without one. */
    isLoading: id ? awaitingRead || awaitingList : list.isLoading,
    isFetching: fetched.isFetching,
    error: fetched.error,
    refetch: fetched.refetch,
  };
}

/** The collection with the ETag a guarded write must send, on the key the editor reads. */
export function useScopeSnapshot<Raw extends WireCollection>(
  scope: CollectionScope<Raw>,
  id: string | undefined,
  { enabled = true, refetchOnMount }: { enabled?: boolean; refetchOnMount?: "always" } = {},
) {
  return useQuery({
    queryKey: scope.keys.snapshot(id ?? ""),
    queryFn: () => scope.fetchSnapshot(id!),
    enabled: enabled && Boolean(id),
    refetchOnMount,
  });
}

/** How many titles a Smart editor's live preview shows. */
export const PREVIEW_LIMIT = 24;
const PREVIEW_DEBOUNCE_MS = 300;

export type ScopePreview =
  | { status: "loading" }
  | { status: "error" }
  | {
      status: "ready";
      items: PreviewItem[];
      total: number;
      /** The titles are for earlier rules; the current ones' are on the way. */
      refreshing: boolean;
    };

/**
 * A live preview of smart rules across every library they name (every library
 * the scope can see when they name none), from the scope's preview route. The rules are debounced so editing doesn't send a
 * request per keystroke, and the last result stays on screen while the next
 * one loads. Nothing is asked while `enabled` is false.
 */
export function useScopePreview<Raw extends WireCollection>(
  scope: CollectionScope<Raw>,
  rules: QueryDefinition,
  { enabled = true }: { enabled?: boolean } = {},
): ScopePreview {
  // A string, so a re-render with equal rules does not restart the wait.
  const current = JSON.stringify(normalizeQueryDefinition(rules));
  const settled = useDebounce(current, PREVIEW_DEBOUNCE_MS);
  const query = useQuery({
    queryKey: scope.keys.preview(`${PREVIEW_LIMIT}:${settled}`),
    queryFn: () => scope.preview(JSON.parse(settled) as QueryDefinition, PREVIEW_LIMIT),
    // Only once the rules have stopped changing; the last result stays meanwhile.
    enabled: enabled && settled === current,
    placeholderData: keepPreviousData,
    staleTime: 30_000,
    retry: false,
  });
  if (query.data) {
    return {
      status: "ready",
      items: query.data.items,
      total: query.data.total,
      refreshing: settled !== current || query.isFetching,
    };
  }
  if (query.isError) return { status: "error" };
  return { status: "loading" };
}

/** Sync now for a synced list: the mutation takes the collection id. */
export function useScopeSync<Raw extends WireCollection>(scope: CollectionScope<Raw>) {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: (id: string) => scope.sync(id),
    onSuccess: (result, id) => {
      const matched = `${result.itemsMatched} item${result.itemsMatched === 1 ? "" : "s"}`;
      toast.success(
        result.status === "warning"
          ? `Synced with warnings — matched ${matched}`
          : `Synced — matched ${matched}`,
      );
      void scope.invalidate(queryClient, id);
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : "Sync failed");
    },
  });
}

/**
 * Deletes a collection with the ETag its caller read. `onDeleted` runs before
 * the scope's queries refresh, so a page showing the collection can leave
 * before its own read answers 404. A 412 refreshes them, so the next try sends
 * the current version; `onStale` lets a caller that keeps its own copy, such
 * as the editor, read it again too.
 */
export function useScopeDelete<Raw extends WireCollection>(
  scope: CollectionScope<Raw>,
  options: { onDeleted?: (id: string) => void; onStale?: () => void } = {},
) {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: (ref: { id: string; etag: string }) => scope.remove(ref),
    onSuccess: (_data, { id }) => {
      toast.success("Collection deleted");
      options.onDeleted?.(id);
      return scope.invalidate(queryClient, id);
    },
    onError: (error) => {
      toast.error(scope.errorMessage(error, "Failed to delete"));
      if (isPreconditionFailed(error)) {
        void scope.invalidate(queryClient);
        options.onStale?.();
      }
    },
  });
}

/** PUT one title into a manual collection at `position` (not guarded; the collection's revision moves). */
export function putCollectionItem<Raw extends WireCollection>(
  scope: CollectionScope<Raw>,
  id: string,
  itemId: string,
  position: number,
) {
  return v2(
    scope.itemSource === "user"
      ? "PUT /api/v2/collections/{id}/items/{item_id}"
      : "PUT /api/v2/admin/collections/{id}/items/{item_id}",
    { path: { id, item_id: itemId }, body: { position } },
  );
}

export function isArtworkStaged(slot: ArtworkSlotDraft | undefined): boolean {
  return Boolean(slot?.file || slot?.sourceUrl?.trim() || slot?.remove);
}

const ARTWORK_SLOTS: readonly ArtworkSlot[] = ["poster", "backdrop"];

function stagedSlots(artwork: ArtworkDraft): ArtworkSlot[] {
  return ARTWORK_SLOTS.filter((slot) => isArtworkStaged(artwork[slot]));
}

function stagedOrNone(itemIds: string[]) {
  return itemIds.length ? itemIds : undefined;
}

function isPreconditionFailed(error: unknown) {
  return error instanceof V2ProblemError && error.status === 412;
}

interface DraftState<Raw extends WireCollection> {
  /** Set once the collection exists. */
  id?: string;
  /** The collection's current ETag; refreshed after every write. */
  etag?: string;
  view?: CollectionView<Raw>;
  /** The collection as last read, as a draft: what `draft` is compared and merged against. */
  base: CollectionDraft;
  draft: CollectionDraft;
  /** Fields both this draft and someone else changed, after a merge. */
  conflicts: DraftField[];
}

export interface CreateResult {
  id: string;
  /** Staged titles that couldn't be added; they stay staged for Try again. */
  failedItems: string[];
  /** A synced list's first sync, when it ran. */
  sync?: SyncOutcome;
  /** What saved with problems after the collection was created. */
  warnings: string[];
}

/**
 * A collection editor's draft: the copy it started from, what the person has
 * changed, and the token its next save sends.
 *
 * Titles save on their own, and each title write moves the collection's
 * revision. `syncWithServer` reads the collection again and merges
 * (`mergeDraft`), so the next Save sends a current ETag and keeps every field
 * the person changed. A save that still answers 412 merges and retries once;
 * a field both sides changed stops the save and is listed in `conflicts`.
 */
export function useCollectionDraft<Raw extends WireCollection>(
  scope: CollectionScope<Raw>,
  init: { snapshot?: EditorSnapshot<Raw>; kind: CreateKind; libraryId?: number | null },
) {
  const queryClient = useQueryClient();
  const [state, setStateValue] = useState<DraftState<Raw>>(() => {
    const draft = scope.toDraft(init.snapshot?.view ?? null, init);
    return {
      id: init.snapshot?.view.id,
      etag: init.snapshot?.etag,
      view: init.snapshot?.view,
      base: draft,
      draft,
      conflicts: [],
    };
  });
  // Merges and saves read the latest state, not the render they started in.
  const current = useRef(state);
  const setState = useCallback((update: (previous: DraftState<Raw>) => DraftState<Raw>) => {
    current.current = update(current.current);
    setStateValue(current.current);
  }, []);
  const [isSaving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);
  const [artworkErrors, setArtworkErrors] = useState<Partial<Record<ArtworkSlot, string>>>({});
  const syncRun = useRef(0);
  const initKind = init.kind;

  const readFresh = useCallback(
    async (id: string, artworkChanged: boolean): Promise<EditorSnapshot<Raw>> => {
      const fresh = await queryClient.fetchQuery({
        queryKey: scope.keys.snapshot(id),
        queryFn: () => scope.fetchSnapshot(id),
        staleTime: 0,
      });
      // The editor read may carry no artwork; after an artwork change the list has the new one.
      const listed = artworkChanged
        ? (
            await queryClient.fetchQuery({
              queryKey: scope.keys.list,
              queryFn: () => scope.fetchList(),
              staleTime: 0,
            })
          ).collections.find((entry) => entry.id === id)
        : current.current.view?.raw;
      return { etag: fresh.etag, view: scope.toView(withListedArtwork(fresh.view.raw, listed)) };
    },
    [queryClient, scope],
  );

  const setDraft = useCallback(
    (update: (draft: CollectionDraft) => CollectionDraft) =>
      setState((previous) => ({ ...previous, draft: update(previous.draft) })),
    [setState],
  );

  /** Reads the collection again and merges it into the draft. */
  const syncWithServer = useCallback(async (): Promise<{ conflicts: DraftField[] }> => {
    const id = current.current.id;
    if (!id) return { conflicts: [] };
    const run = ++syncRun.current;
    const fresh = await readFresh(id, false);
    // A later read answers for both.
    if (run !== syncRun.current) return { conflicts: current.current.conflicts };
    const theirs = scope.toDraft(fresh.view, { kind: initKind });
    const merged = mergeDraft(current.current.base, current.current.draft, theirs);
    // A conflict still waiting for Keep mine or Use theirs stays one: the new
    // base already holds their value, so the merge alone would read it as only mine.
    const unresolved = current.current.conflicts;
    const conflicts = changedFields(merged.base, merged.draft).filter(
      (field) => merged.conflicts.includes(field) || unresolved.includes(field),
    );
    setState((previous) => ({
      ...previous,
      etag: fresh.etag,
      view: fresh.view,
      base: merged.base,
      draft: { ...merged.draft, stagedItems: previous.draft.stagedItems },
      conflicts,
    }));
    return { conflicts };
  }, [initKind, readFresh, scope, setState]);

  /**
   * The artwork a save leaves staged: an image it couldn't upload stays, with
   * its message, and an image changed while it ran stays as it is now.
   */
  const keepFailedArtwork = useCallback(
    (saved: CollectionDraft, failedArtwork: ArtworkSlot[], warnings: string[]) => {
      const now = current.current.draft.artwork;
      const edited = ARTWORK_SLOTS.filter((slot) => now[slot] !== saved.artwork[slot]);
      const failed = failedArtwork.filter((slot) => !edited.includes(slot));
      setArtworkErrors(
        Object.fromEntries(
          failed.map((slot) => [
            slot,
            warnings.find((warning) => warning.startsWith(`${slot}:`)) ?? warnings[0] ?? "",
          ]),
        ),
      );
      return Object.fromEntries([
        ...failed.map((slot) => [slot, saved.artwork[slot]]),
        ...edited.flatMap((slot) => (now[slot] ? [[slot, now[slot]]] : [])),
      ]) as ArtworkDraft;
    },
    [],
  );

  /**
   * After a save: the fresh copy is the new base; edits made while saving stay.
   * `stillStaged`, when given, replaces the staged titles in the same update.
   */
  const rebase = useCallback(
    async (
      id: string,
      saved: CollectionDraft,
      failedArtwork: ArtworkSlot[],
      warnings: string[],
      stillStaged?: string[],
    ) => {
      await scope.invalidate(queryClient, id);
      const fresh = await readFresh(id, stagedSlots(saved.artwork).length > 0);
      const theirs = scope.toDraft(fresh.view, { kind: initKind });
      const kept = keepFailedArtwork(saved, failedArtwork, warnings);
      setState((previous) => {
        const later = mergeDraft(saved, previous.draft, theirs).draft;
        return {
          ...previous,
          id,
          etag: fresh.etag,
          view: fresh.view,
          base: theirs,
          draft: {
            ...later,
            artwork: kept,
            stagedItems: stillStaged ? stagedOrNone(stillStaged) : previous.draft.stagedItems,
            // A new Synced list's step isn't on the saved collection; it stays until the page leaves.
            synced: previous.draft.synced,
          },
          conflicts: [],
        };
      });
    },
    [initKind, keepFailedArtwork, queryClient, readFresh, scope, setState],
  );

  const addStaged = useCallback(
    async (id: string, itemIds: readonly string[], firstPosition: number) => {
      const failed: string[] = [];
      for (const [index, itemId] of itemIds.entries()) {
        try {
          await putCollectionItem(scope, id, itemId, firstPosition + index);
        } catch {
          failed.push(itemId);
        }
      }
      return failed;
    },
    [scope],
  );

  /**
   * Creates the collection, then adds its staged titles in order. The editor
   * stays in create mode until it has read the new collection back. When only
   * that read fails, the collection still counts as created: the draft as sent
   * becomes the base, and the next save reads the collection first.
   */
  const create = useCallback(async (): Promise<CreateResult | null> => {
    // Created already: a second Create would make a second collection.
    if (current.current.id) return null;
    const saved = current.current.draft;
    setSaving(true);
    setSaveError(null);
    try {
      const outcome = await scope.create(saved as CreatableDraft);
      const failedItems = await addStaged(outcome.id, saved.stagedItems ?? [], 0);
      try {
        await rebase(outcome.id, saved, outcome.failedArtwork, outcome.warnings, failedItems);
      } catch {
        const kept = keepFailedArtwork(saved, outcome.failedArtwork, outcome.warnings);
        setState((previous) => ({
          ...previous,
          id: outcome.id,
          base: { ...saved, artwork: {} },
          draft: { ...previous.draft, artwork: kept, stagedItems: stagedOrNone(failedItems) },
          conflicts: [],
        }));
      }
      return { id: outcome.id, failedItems, sync: outcome.sync, warnings: outcome.warnings };
    } catch (error) {
      setSaveError(scope.errorMessage(error, SAVE_FAILED));
      return null;
    } finally {
      setSaving(false);
    }
  }, [addStaged, keepFailedArtwork, rebase, scope, setState]);

  /** Tries the titles that failed after create again, at the end of the collection. */
  const retryStagedItems = useCallback(
    async (firstPosition: number) => {
      const { id, draft } = current.current;
      if (!id || !draft.stagedItems?.length) return;
      const failed = await addStaged(id, draft.stagedItems, firstPosition);
      setDraft((next) => ({ ...next, stagedItems: stagedOrNone(failed) }));
      await scope.invalidate(queryClient, id);
      await syncWithServer();
    },
    [addStaged, queryClient, scope, setDraft, syncWithServer],
  );

  /**
   * Saves the draft with the current ETag. A 412 merges the newer copy; with no
   * conflicting field the save is retried once, otherwise it stops at the conflict.
   */
  const save = useCallback(async (): Promise<boolean> => {
    const id = current.current.id;
    if (!id) return false;
    setSaving(true);
    setSaveError(null);
    try {
      // Created, but never read back: read it before sending a version.
      if (!current.current.etag) {
        const { conflicts } = await syncWithServer();
        if (conflicts.length > 0) return false;
      }
      for (let attempt = 0; ; attempt++) {
        const { draft, etag, base } = current.current;
        try {
          const outcome = await scope.update({ id, etag: etag ?? "" }, draft as SavableDraft, base);
          await rebase(id, draft, outcome.failedArtwork, outcome.warnings);
          return true;
        } catch (error) {
          if (!isPreconditionFailed(error)) throw error;
          const { conflicts } = await syncWithServer();
          if (conflicts.length > 0) return false;
          if (attempt > 0) {
            // Changed again under the retry: let the person decide.
            setState((previous) => ({
              ...previous,
              conflicts: changedFields(previous.base, previous.draft),
            }));
            return false;
          }
        }
      }
    } catch (error) {
      setSaveError(scope.errorMessage(error, SAVE_FAILED));
      return false;
    } finally {
      setSaving(false);
    }
  }, [rebase, scope, setState, syncWithServer]);

  const resolveConflicts = useCallback(
    (take: "mine" | "theirs") =>
      setState((previous) => ({
        ...previous,
        draft:
          take === "theirs"
            ? takeFields(previous.draft, previous.base, previous.conflicts)
            : previous.draft,
        conflicts: [],
      })),
    [setState],
  );

  /** Back to the collection as last read. Titles are already saved and stay. */
  const discard = useCallback(() => {
    setArtworkErrors({});
    setSaveError(null);
    setState((previous) => ({
      ...previous,
      draft: { ...previous.base, stagedItems: previous.draft.stagedItems },
      conflicts: [],
    }));
  }, [setState]);

  const changed = changedFields(state.base, state.draft);
  const artwork = stagedSlots(state.draft.artwork);
  return {
    ...state,
    setDraft,
    changed,
    /** What the save bar names: changed fields, then staged artwork. */
    pendingLabels: [
      ...new Set([
        ...changed.map((field) => DRAFT_FIELD_LABEL[field]),
        ...artwork.map((slot) => ARTWORK_SLOT_LABEL[slot]),
      ]),
    ],
    isDirty: changed.length > 0 || artwork.length > 0,
    isSaving,
    saveError,
    artworkErrors,
    syncWithServer,
    create,
    save,
    retryStagedItems,
    resolveConflicts,
    discard,
  };
}
