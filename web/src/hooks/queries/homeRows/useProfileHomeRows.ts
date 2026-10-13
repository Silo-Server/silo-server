import { useCallback, useMemo, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  type ProfileRequestContextSnapshot,
} from "@/api/client";
import type { SectionOverride, SettingsSectionEntry } from "@/api/types";
import { sectionKeys } from "@/hooks/queries/keys";
import {
  replaceProfileSectionOverrides,
  resetProfileSectionOverrides,
  useProfileSectionOverrides,
  useProfileSectionSettings,
} from "@/hooks/queries/sections";
import { HOME_PAGE, samePage } from "@/lib/homeRows/pages";
import { nextAppendPosition } from "@/lib/homeRows/payloads";
import {
  applySectionDeletion,
  buildSectionOverrides,
  canMutateSectionSettings,
  createOverrideIdSource,
  hydrateRemovedSystemSections,
  sectionSaveErrorMessage,
  type RemovedSystemOverride,
} from "@/lib/homeRows/profileOverrides";
import type { PageRef } from "@/lib/homeRows/types";

/** One page as the profile edits it: its rows in order and the server rows it removed. */
interface PageState {
  sections: SettingsSectionEntry[];
  removed: RemovedSystemOverride[];
}

/** `profile` is the household profile that made the change; only it is written. */
type QueueEntry = { page: PageRef; profile: ProfileRequestContextSnapshot } & (
  | {
      kind: "save";
      state: PageState;
      /** The page as read when the draft was started, which the save compares it to. */
      baseline: SettingsSectionEntry[];
      changedIds: Set<string>;
    }
  | { kind: "reset" }
);

interface SaveQueue {
  running: boolean;
  /** The newest state not yet sent; a later change replaces it. */
  next: QueueEntry | null;
}

const NO_SECTIONS: SettingsSectionEntry[] = [];

function idList(ids: string | readonly string[]): readonly string[] {
  return typeof ids === "string" ? [ids] : ids;
}

function pageQuery(page: PageRef) {
  return {
    scope: page.kind,
    libraryId: page.kind === "library" ? page.libraryId : undefined,
    libraryKey: page.kind === "library" ? String(page.libraryId) : undefined,
  };
}

export interface ProfileHomeRows {
  page: PageRef;
  /** Returns false, and stays on this page, while this page still has saves to send. */
  setPage(ref: PageRef): boolean;
  scope: "home" | "library";
  libraryId: number | undefined;
  /** The page's rows in order, including changes not saved yet. */
  sections: SettingsSectionEntry[];
  /** The page's rows as last read, without changes not saved yet. */
  savedSections: SettingsSectionEntry[];
  /** Both the rows and the saved overrides have loaded. */
  ready: boolean;
  /** The rows are still loading for the first time. */
  loading: boolean;
  /** Why the rows failed to load, when they did and none are on screen. */
  loadError: Error | null;
  /** Reads the rows and the saved overrides again. */
  reload(): Promise<void>;
  /** Changes save only when the page is ready and no reset is in flight. */
  canEdit: boolean;
  /** The saved overrides failed to load, so editing stays off. */
  overridesFailed: boolean;
  /** A save or reset, or the refetch after it, is still in flight. */
  pending: boolean;
  setHidden(id: string, hidden: boolean): void;
  /**
   * Moves `activeId` to where `orderedIds` (the order the user saw, after the
   * move) puts it: before the next row there still on the page, else after
   * the one before it. Rows that came or went since then don't shift it.
   */
  move(activeId: string, orderedIds: readonly string[]): void;
  /** Replaces the row with the same id, or adds it at the bottom. */
  saveSection(section: SettingsSectionEntry): void;
  /** Removes server rows from this page and deletes the profile's own rows, in one save. */
  remove(ids: string | readonly string[]): void;
  /** Drops every override this profile saved for the page. */
  reset(): void;
  /** When this tab last saved a change to the row, in ms since the epoch. */
  lastWriteAt(rowId: string): number | undefined;
}

/**
 * Settings > Home Screen: one page's rows for this profile, and every write to
 * them. A save replaces the page's whole override set and the server checks no
 * version, so saves go out one at a time: changes made while one is in flight
 * merge into a single next save that carries every row they touched, and the
 * newest state wins, naming the rows of the saves before it. A failed save
 * puts the last saved state back unless a newer change is still to be sent,
 * and edits then wait for the refetch, since the save may have landed. A
 * change is written only as the profile that made it: one still queued when
 * another profile is picked is dropped, and that profile's first change starts
 * from its own rows. The page can't be switched until its saves and the
 * refetch after them land.
 */
export function useProfileHomeRows(): ProfileHomeRows {
  const queryClient = useQueryClient();
  const [page, setPageState] = useState<PageRef>(HOME_PAGE);
  const { scope, libraryId } = pageQuery(page);
  const settingsQuery = useProfileSectionSettings(scope, libraryId);
  const rawOverridesQuery = useProfileSectionOverrides(scope, libraryId);

  const [draft, setDraftState] = useState<PageState | null>(null);
  // Mirrors `draft` so changes made in one event build on each other.
  const draftRef = useRef<PageState | null>(null);
  // The profile whose changes the draft holds; another profile's change starts over.
  const draftProfile = useRef<ProfileRequestContextSnapshot | null>(null);
  // The page as read when the draft was started. A refetch between saves may
  // bring an admin's edit; comparing the draft to it would pin the old value.
  const draftBaseline = useRef<SettingsSectionEntry[]>(NO_SECTIONS);
  const [pending, setPending] = useState(false);
  // Edits wait for the read after a reset or a failed save: until then they
  // would be built on rows and overrides the server may already have changed.
  const [editsHeld, setEditsHeld] = useState(false);
  const queue = useRef<SaveQueue>({ running: false, next: null });
  // New override IDs for admin rows on this page, reused until the page changes.
  const newOverrideId = useRef(createOverrideIdSource());
  const writes = useRef(new Map<string, number>());

  const ready = canMutateSectionSettings(settingsQuery, rawOverridesQuery);
  const canEdit = ready && !editsHeld;

  const setDraft = useCallback((next: PageState | null) => {
    draftRef.current = next;
    setDraftState(next);
  }, []);

  const serverSections = settingsQuery.data?.sections;
  const savedOverrides = rawOverridesQuery.data?.overrides;
  const sections = useMemo(() => draft?.sections ?? serverSections ?? [], [draft, serverSections]);

  const send = useCallback(
    async (entry: QueueEntry) => {
      const { scope, libraryKey } = pageQuery(entry.page);
      if (entry.kind === "reset") {
        await resetProfileSectionOverrides({
          scope,
          libraryId: libraryKey,
          profileContext: entry.profile,
        });
        return;
      }
      // Built when sent, against the overrides as last read, so it keeps the
      // override IDs the save before it stored; and against the page the draft
      // started from, so it stores only what this profile changed.
      const overrides = buildSectionOverrides(entry.state.sections, entry.state.removed, {
        savedOverrides: queryClient.getQueryData<{ overrides: SectionOverride[] }>(
          sectionKeys.profileOverridesRaw(scope, libraryKey),
        )?.overrides,
        baseline: entry.baseline,
        newId: newOverrideId.current,
        changedSectionIds: entry.changedIds,
      });
      await replaceProfileSectionOverrides({
        scope,
        library_id: libraryKey,
        overrides,
        profileContext: entry.profile,
      });
      const now = Date.now();
      for (const id of entry.changedIds) writes.current.set(id, now);
    },
    [queryClient],
  );

  const drain = useCallback(async () => {
    const q = queue.current;
    if (q.running) return;
    q.running = true;
    setPending(true);
    while (q.next) {
      const entry = q.next;
      q.next = null;
      // The cached overrides it builds on now belong to the other profile.
      if (!isCapturedProfileAuthorityActive(entry.profile)) continue;
      try {
        await send(entry);
        if (entry.kind === "reset") toast.success("Reset to the server's rows.");
      } catch (error) {
        if (entry.kind === "reset") {
          toast.error("Could not reset your rows");
        } else {
          toast.error(sectionSaveErrorMessage(error));
          // With no newer change to send, show the last saved state again. A
          // failed save may still have landed, so edits wait for the refetch.
          // Read through the ref: a newer change may have arrived during the await.
          if (!queue.current.next) {
            setDraft(null);
            setEditsHeld(true);
          }
        }
      }
      // A failed save may still have landed, so refetch after every attempt.
      await queryClient.invalidateQueries({ queryKey: sectionKeys.all });
      // A newer change by the same profile carries the user's latest state,
      // this save's edits included, so it names this save's rows too: the
      // overrides it builds on may not hold them yet if the save failed or
      // the refetch did. Read through the ref, as above.
      const newer = queue.current.next;
      if (
        entry.kind === "save" &&
        newer?.kind === "save" &&
        newer.profile.profileId === entry.profile.profileId
      ) {
        for (const id of entry.changedIds) newer.changedIds.add(id);
      }
    }
    q.running = false;
    setDraft(null);
    setEditsHeld(false);
    setPending(false);
  }, [queryClient, send, setDraft]);

  const change = useCallback(
    (ids: string | readonly string[], edit: (state: PageState) => PageState | null) => {
      if (!canEdit) return;
      const profile = captureProfileRequestContext();
      if (!profile) return;
      const current =
        draftProfile.current && isCapturedProfileAuthorityActive(draftProfile.current)
          ? draftRef.current
          : null;
      const baseline = current ? draftBaseline.current : (serverSections ?? []);
      const base = current ?? {
        sections: baseline,
        removed: hydrateRemovedSystemSections(savedOverrides),
      };
      const next = edit(base);
      if (!next) return;
      setDraft(next);
      draftProfile.current = profile;
      draftBaseline.current = baseline;
      const q = queue.current;
      // A queued save by another profile is dropped, and its rows with it.
      const queued =
        q.next?.kind === "save" && q.next.profile.profileId === profile.profileId
          ? q.next.changedIds
          : [];
      const changedIds = new Set(queued);
      for (const id of idList(ids)) changedIds.add(id);
      q.next = { kind: "save", page, profile, state: next, baseline, changedIds };
      void drain();
    },
    [canEdit, drain, page, savedOverrides, serverSections, setDraft],
  );

  const setHidden = useCallback(
    (id: string, hidden: boolean) =>
      change(id, (state) => ({
        ...state,
        sections: state.sections.map((s) => (s.id === id ? { ...s, hidden } : s)),
      })),
    [change],
  );

  const move = useCallback(
    (activeId: string, orderedIds: readonly string[]) =>
      change(activeId, (state) => {
        const moved = state.sections.find((s) => s.id === activeId);
        const at = orderedIds.indexOf(activeId);
        if (!moved || at === -1) return null;
        const rest = state.sections.filter((s) => s !== moved);
        const indexOf = (id: string) => rest.findIndex((s) => s.id === id);
        const after = orderedIds.slice(at + 1).find((id) => indexOf(id) !== -1);
        const before = orderedIds
          .slice(0, at)
          .reverse()
          .find((id) => indexOf(id) !== -1);
        let to: number;
        if (after !== undefined) to = indexOf(after);
        else if (before !== undefined) to = indexOf(before) + 1;
        else return null;
        const sections = [...rest.slice(0, to), moved, ...rest.slice(to)];
        if (sections.every((s, index) => s === state.sections[index])) return null;
        return { ...state, sections };
      }),
    [change],
  );

  const saveSection = useCallback(
    (section: SettingsSectionEntry) =>
      change(section.id, (state) => ({
        ...state,
        sections: state.sections.some((s) => s.id === section.id)
          ? state.sections.map((s) => (s.id === section.id ? section : s))
          : [
              ...state.sections,
              { ...section, position: nextAppendPosition(state.sections.map((s) => s.position)) },
            ],
      })),
    [change],
  );

  // A removed row is not on the page any more, so naming it changes nothing
  // the save leaves out; it only records the write. Several go in one save:
  // the server refuses a save that still holds any rule row while rule rows
  // are off, so deleting them one save at a time would never get through.
  const remove = useCallback(
    (ids: string | readonly string[]) =>
      change(ids, (state) =>
        idList(ids).reduce<PageState>((current, id) => {
          const next = applySectionDeletion(current.sections, current.removed, id);
          return { sections: next.sections, removed: next.removedSystemSections };
        }, state),
      ),
    [change],
  );

  const reset = useCallback(() => {
    if (!canEdit) return;
    const profile = captureProfileRequestContext();
    if (!profile) return;
    // Edits wait for the reset: until the page refetches they would be built on
    // the overrides it drops and save them again.
    setEditsHeld(true);
    queue.current.next = { kind: "reset", page, profile };
    void drain();
  }, [canEdit, drain, page]);

  const setPage = useCallback(
    (ref: PageRef) => {
      if (queue.current.running) return false;
      if (samePage(ref, page)) return true;
      newOverrideId.current = createOverrideIdSource();
      setDraft(null);
      setPageState(ref);
      return true;
    },
    [page, setDraft],
  );

  const lastWriteAt = useCallback((rowId: string) => writes.current.get(rowId), []);

  const { refetch: refetchSettings } = settingsQuery;
  const { refetch: refetchOverrides } = rawOverridesQuery;
  const reload = useCallback(async () => {
    await Promise.all([refetchSettings(), refetchOverrides()]);
  }, [refetchOverrides, refetchSettings]);

  return {
    page,
    setPage,
    scope,
    libraryId,
    sections,
    savedSections: serverSections ?? NO_SECTIONS,
    ready,
    loading: settingsQuery.isLoading,
    loadError: settingsQuery.data ? null : settingsQuery.error,
    reload,
    canEdit,
    overridesFailed: rawOverridesQuery.isError,
    pending,
    setHidden,
    move,
    saveSection,
    remove,
    reset,
    lastWriteAt,
  };
}
