import { useEffect, useRef } from "react";

/**
 * Moves focus to the first item a Load more press added to a list, so a
 * keyboard or screen reader user lands on what the press brought. Call press()
 * as the button asks for more. Once the load it started has finished (loading
 * went false again, or count grew without one), the first link in the list's
 * child at the old count takes focus. A viewer who moved focus elsewhere in the
 * meantime keeps it there; focus on the body counts as not moved, because that
 * is where it lands when the button goes with nothing left to load.
 */
export function useLoadMoreFocus<T extends HTMLElement>(loading: boolean, count: number) {
  const listRef = useRef<T>(null);
  const buttonRef = useRef<HTMLButtonElement>(null);
  const pending = useRef<{ from: number; started: boolean }>(undefined);
  useEffect(() => {
    const press = pending.current;
    if (!press) return;
    if (loading) {
      press.started = true;
      return;
    }
    if (!press.started && count <= press.from) return;
    pending.current = undefined;
    const focused = document.activeElement;
    if (focused !== buttonRef.current && focused !== document.body) return;
    listRef.current?.children[press.from]?.querySelector<HTMLElement>("a[href]")?.focus();
  });
  const press = () => {
    pending.current = { from: count, started: false };
  };
  return { listRef, buttonRef, press };
}
