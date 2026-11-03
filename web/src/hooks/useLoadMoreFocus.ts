import { useEffect, useRef } from "react";

/**
 * Moves focus to the first item a Load more press added to a list, so a
 * keyboard or screen reader user lands on what the press brought. Call press()
 * as the button asks for more. Once the load it started has finished (loading
 * went false again, or the list grew without one), the first link in the first
 * child the list gained takes focus. The list's children are counted, so items
 * rendered ahead of the loaded ones need no bookkeeping. A viewer who moved
 * focus elsewhere in the meantime keeps it there; focus on the body counts as
 * not moved, because that is where it lands when the button goes with nothing
 * left to load. If the press added nothing and took the button with it (its
 * load failed), the last link in fallbackRef takes focus instead.
 */
export function useLoadMoreFocus<T extends HTMLElement>(loading: boolean) {
  const listRef = useRef<T>(null);
  const buttonRef = useRef<HTMLButtonElement>(null);
  const fallbackRef = useRef<HTMLDivElement>(null);
  const pending = useRef<{ from: number; started: boolean }>(undefined);
  useEffect(() => {
    const press = pending.current;
    if (!press) return;
    if (loading) {
      press.started = true;
      return;
    }
    const list = listRef.current;
    if (!press.started && (list?.children.length ?? 0) <= press.from) return;
    pending.current = undefined;
    const focused = document.activeElement;
    if (focused !== buttonRef.current && focused !== document.body) return;
    const added = list?.children[press.from]?.querySelector<HTMLElement>("a[href]");
    if (added) added.focus();
    else if (focused === document.body)
      fallbackRef.current?.querySelector<HTMLElement>("a[href]:last-of-type")?.focus();
  });
  const press = () => {
    pending.current = { from: listRef.current?.children.length ?? 0, started: false };
  };
  return { listRef, buttonRef, fallbackRef, press };
}
