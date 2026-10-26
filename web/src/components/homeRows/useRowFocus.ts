import { useCallback, useEffect, useRef } from "react";
import type { HomeRow } from "@/lib/homeRows/types";

type FocusTarget = { rowId: string | null; waitForGone?: string };

export interface RowFocus {
  /** Ref for a row's ⋯ trigger. */
  attachMenuTrigger: (rowId: string) => (element: HTMLButtonElement | null) => void;
  /** Ref for the page's add button, the target when the last row goes. */
  attachAddButton: (element: HTMLButtonElement | null) => void;
  /** After a delete lands: the next row's ⋯, else the previous row's, else the add button. */
  afterRemoval: (rowId: string) => void;
  /** After a move lands: the moved row's ⋯, wherever the refetch puts it. */
  afterMove: (rowId: string) => void;
  /** Now: the row's ⋯, for a dialog opened from it that closes with the row still there. */
  returnToMenu: (rowId: string) => void;
}

/**
 * Puts keyboard focus back somewhere sensible after a row is deleted or moved.
 * Focus moves once the write and its refetch have settled, because until then
 * the deleted row is still on screen and a moved row may still jump.
 */
export function useRowFocus(rows: HomeRow[], pending: boolean): RowFocus {
  const triggers = useRef(new Map<string, HTMLButtonElement>());
  const refCallbacks = useRef(new Map<string, (element: HTMLButtonElement | null) => void>());
  const addButton = useRef<HTMLButtonElement | null>(null);
  // A ref, not state: every request comes with a write that changes `rows` or
  // `pending`, which is what re-runs the effect below.
  const target = useRef<FocusTarget | null>(null);

  const attachMenuTrigger = useCallback((rowId: string) => {
    let callback = refCallbacks.current.get(rowId);
    if (!callback) {
      callback = (element) => {
        if (element) triggers.current.set(rowId, element);
        else triggers.current.delete(rowId);
      };
      refCallbacks.current.set(rowId, callback);
    }
    return callback;
  }, []);

  useEffect(() => {
    const wanted = target.current;
    if (!wanted || pending) return;
    if (wanted.waitForGone && rows.some((row) => row.id === wanted.waitForGone)) return;
    if (wanted.rowId === null) {
      addButton.current?.focus();
    } else {
      const trigger = triggers.current.get(wanted.rowId);
      if (!trigger) return;
      trigger.focus();
    }
    target.current = null;
  }, [rows, pending]);

  const afterRemoval = useCallback(
    (rowId: string) => {
      const index = rows.findIndex((row) => row.id === rowId);
      const next = rows[index + 1] ?? rows[index - 1];
      target.current = { rowId: index === -1 ? null : (next?.id ?? null), waitForGone: rowId };
    },
    [rows],
  );

  const afterMove = useCallback((rowId: string) => {
    target.current = { rowId };
  }, []);

  const attachAddButton = useCallback((element: HTMLButtonElement | null) => {
    addButton.current = element;
  }, []);

  const returnToMenu = useCallback((rowId: string) => {
    triggers.current.get(rowId)?.focus();
  }, []);

  return { attachMenuTrigger, attachAddButton, afterRemoval, afterMove, returnToMenu };
}
