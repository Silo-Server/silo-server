import { useQueryClient } from "@tanstack/react-query";
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { toast } from "sonner";

import type { AdminUser, UpdateUserRequest } from "@/api/types";
import { adminUserScope, getAdminUser, type AdminUserEditor } from "@/api/v2/adminUsers";
import { V2ProblemError } from "@/api/v2/request";
import { useUpdateUser } from "@/hooks/queries/admin/users";
import { adminKeys } from "@/hooks/queries/keys";

import { useCardEditing, type AccessCardId } from "../cardEditing";

function isConflict(error: unknown): boolean {
  return error instanceof V2ProblemError && error.status === 412;
}

/** A write a card makes besides the account PUT (the Requests card's own limit). */
export interface CardExtraWrite<D> {
  /** Whether the draft needs this write. */
  changed: (draft: D) => boolean;
  /** Runs before the account PUT; a 412 it throws becomes the card's conflict. */
  write: (draft: D) => Promise<void>;
  /**
   * Re-reads what the write is validated against, for "Reload and review".
   * It may return a rebase for the draft's own part, applied after the
   * account fields are rebased.
   */
  reload: () => Promise<((draft: D) => D) | void>;
  /** Names what the write saved, for a save that fails after it ("Request limit"). */
  savedLabel?: string;
}

/** Structural equality for draft values: plain objects, arrays and primitives. */
export function sameDraftValue(a: unknown, b: unknown): boolean {
  if (Object.is(a, b)) return true;
  if (typeof a !== "object" || typeof b !== "object" || a === null || b === null) return false;
  if (Array.isArray(a) !== Array.isArray(b)) return false;
  if (Array.isArray(a) && Array.isArray(b)) {
    return a.length === b.length && a.every((item, i) => sameDraftValue(item, b[i]));
  }
  const ra = a as Record<string, unknown>;
  const rb = b as Record<string, unknown>;
  const keys = new Set([...Object.keys(ra), ...Object.keys(rb)]);
  for (const key of keys) {
    if (!sameDraftValue(ra[key], rb[key])) return false;
  }
  return true;
}

/**
 * Rebases a draft onto a newer account, field by field: a field the admin
 * left as it was takes the newer value, and a field the admin edited keeps
 * the edit. Saving the result then sends only the admin's own changes, so
 * another admin's change made in the meantime survives.
 */
export function rebaseDraft<D>(draft: D, oldBase: D, freshBase: D): D {
  if (typeof draft !== "object" || draft === null) {
    return sameDraftValue(draft, oldBase) ? freshBase : draft;
  }
  const next = { ...draft } as Record<string, unknown>;
  const old = oldBase as Record<string, unknown>;
  const fresh = freshBase as Record<string, unknown>;
  for (const key of Object.keys(next)) {
    if (sameDraftValue(next[key], old[key])) next[key] = fresh[key];
  }
  return next as D;
}

export interface AccountCardDraft<D> {
  editing: boolean;
  draft: D | undefined;
  setDraft(update: (d: D) => D): void;
  /** The account the edit started from (or was last reloaded as). */
  base: AdminUser | undefined;
  /** Labels of the rows that differ from `base`. */
  changed: string[];
  conflict: boolean;
  saving: boolean;
  error: string;
  /** Bumps on each reload, to remount inputs that keep their own state. */
  revision: number;
  start(): void;
  cancel(): void;
  save(): Promise<boolean>;
  reload(): Promise<void>;
}

/**
 * One Access & limits card's edit: it captures the account and its ETag when
 * editing starts, saves only the fields that changed in one PUT with that
 * ETag, and on a 412 keeps the draft and refuses to save until the admin
 * reloads, which swaps in the newer account and rebases the draft onto it:
 * the admin's own edits stay, and every other field takes the newer value.
 */
export function useAccountCardDraft<D>(opts: {
  id: AccessCardId;
  editor: AdminUserEditor | undefined;
  toDraft: (user: AdminUser) => D;
  toBody: (draft: D, base: AdminUser) => UpdateUserRequest;
  changedRows: (draft: D, base: D) => string[];
  validate?: (draft: D) => string | null;
  extra?: CardExtraWrite<D>;
  /**
   * Rebases the draft onto a reloaded account (old and fresh are toDraft of
   * each); defaults to a field-by-field rebase.
   */
  rebase?: (draft: D, oldBase: D, freshBase: D) => D;
}): AccountCardDraft<D> {
  const { begin, update, end } = useCardEditing();
  const updateUser = useUpdateUser();
  const queryClient = useQueryClient();
  const [captured, setCaptured] = useState<AdminUserEditor | null>(null);
  const [draft, setDraftState] = useState<D | undefined>(undefined);
  const [conflict, setConflict] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [revision, setRevision] = useState(0);
  const busy = useRef(false);

  const editing = captured !== null && draft !== undefined;
  const changed = editing ? opts.changedRows(draft, opts.toDraft(captured.user)) : [];
  const changeCount = changed.length;

  // The latest state and options, for the save and discard the page's
  // unsaved-changes prompt calls long after start() handed them over.
  const latest = useRef({ captured, draft, conflict, opts });
  useLayoutEffect(() => {
    latest.current = { captured, draft, conflict, opts };
  });

  const reset = useCallback(() => {
    setCaptured(null);
    setDraftState(undefined);
    setConflict(false);
    setError("");
  }, []);

  const cancel = useCallback(() => {
    if (busy.current) return;
    reset();
    end(latest.current.opts.id);
  }, [end, reset]);

  const save = useCallback(async (): Promise<boolean> => {
    const { captured, draft, conflict, opts } = latest.current;
    if (!captured || draft === undefined) return true;
    if (busy.current || conflict) return false;
    const invalid = opts.validate?.(draft) ?? null;
    if (invalid) {
      setError(invalid);
      return false;
    }
    const body = opts.toBody(draft, captured.user);
    const hasBody = Object.keys(body).length > 0;
    const extra = opts.extra?.changed(draft) ? opts.extra : undefined;
    if (!hasBody && !extra) {
      reset();
      end(opts.id);
      return true;
    }
    busy.current = true;
    setSaving(true);
    setError("");
    // The extra write lands first; if the account PUT then fails, say so
    // instead of reporting the whole save as failed.
    let extraSaved = false;
    try {
      if (extra) {
        await extra.write(draft);
        extraSaved = true;
      }
      if (hasBody) await updateUser.mutateAsync({ editor: captured, body });
      reset();
      end(opts.id);
      toast.success("Saved");
      return true;
    } catch (err) {
      const partly = extraSaved ? `${opts.extra?.savedLabel ?? "Part of this card"} saved. ` : "";
      if (isConflict(err)) {
        setConflict(true);
        if (partly)
          setError(`${partly}The account change wasn't, because another admin changed it.`);
      } else {
        const reason = err instanceof Error ? err.message : "Could not save this account.";
        setError(partly ? `${partly}The account change wasn't: ${reason}` : reason);
      }
      return false;
    } finally {
      busy.current = false;
      setSaving(false);
    }
  }, [end, reset, updateUser]);

  const start = useCallback(() => {
    const { opts } = latest.current;
    if (!opts.editor || busy.current) return;
    const began = begin({
      id: opts.id,
      changeCount: 0,
      save: () => save(),
      discard: () => cancel(),
    });
    if (!began) return;
    setCaptured(opts.editor);
    setDraftState(opts.toDraft(opts.editor.user));
    setConflict(false);
    setError("");
  }, [begin, cancel, save]);

  const reload = useCallback(async () => {
    const { captured, opts } = latest.current;
    if (!captured || busy.current) return;
    busy.current = true;
    setSaving(true);
    try {
      const fresh = await getAdminUser(captured.user.id, captured.profileContext);
      const rebaseExtra = await opts.extra?.reload();
      const rebase = opts.rebase ?? rebaseDraft;
      const oldBase = opts.toDraft(captured.user);
      const freshBase = opts.toDraft(fresh.user);
      setCaptured(fresh);
      setDraftState((prev) => {
        if (prev === undefined) return prev;
        const rebased = rebase(prev, oldBase, freshBase);
        return rebaseExtra ? rebaseExtra(rebased) : rebased;
      });
      setRevision((n) => n + 1);
      setConflict(false);
      setError("");
      // The rest of the page (tab counts, other cards) catches up too.
      void queryClient.invalidateQueries({
        queryKey: [...adminKeys.users(), adminUserScope(captured.profileContext)],
      });
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not reload this account.");
    } finally {
      busy.current = false;
      setSaving(false);
    }
  }, [queryClient]);

  const setDraft = useCallback((change: (d: D) => D) => {
    // A save resets the draft when it lands and a reload rebases it, so edits
    // made while either is in flight are refused rather than silently dropped.
    if (busy.current) return;
    setDraftState((prev) => (prev === undefined ? prev : change(prev)));
  }, []);

  useEffect(() => {
    if (editing) update(opts.id, changeCount);
  }, [editing, changeCount, opts.id, update]);

  // A card that unmounts mid-edit (its tab closed after a discard) lets go.
  useEffect(() => {
    const id = opts.id;
    return () => end(id);
  }, [end, opts.id]);

  return {
    editing,
    draft,
    setDraft,
    base: captured?.user,
    changed,
    conflict,
    saving,
    error,
    revision,
    start,
    cancel,
    save,
    reload,
  };
}
