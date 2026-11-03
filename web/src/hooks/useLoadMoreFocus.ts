import { useEffect, useRef } from "react";

/**
 * Moves focus to the first item a Load more press added to a list, so a
 * keyboard or screen reader user lands on what the press brought. Call press()
 * as the button asks for more. Once the load it started has finished (loading
 * went false again, or an item followed the list's last one without one), the
 * first link in the item after the press-time last one takes focus. Anchoring
 * on that item, not a count, keeps items added ahead of it (cards that load
 * separately at the top of the list) from shifting the target. A viewer who
 * moved focus elsewhere in the meantime keeps it there; focus on the body
 * counts as not moved, because that is where it lands when the button goes
 * with nothing left to load. If the press added nothing and took the button
 * with it (its load failed), the last link in fallbackRef takes focus instead.
 */
export function useLoadMoreFocus<T extends HTMLElement>(loading: boolean) {
  const listRef = useRef<T>(null);
  const buttonRef = useRef<HTMLButtonElement>(null);
  const fallbackRef = useRef<HTMLDivElement>(null);
  const pending = useRef<{ last: Element | null; started: boolean }>(undefined);
  useEffect(() => {
    const press = pending.current;
    if (!press) return;
    if (loading) {
      press.started = true;
      return;
    }
    const first = press.last
      ? press.last.nextElementSibling
      : (listRef.current?.firstElementChild ?? null);
    if (!press.started && !first) return;
    pending.current = undefined;
    const focused = document.activeElement;
    if (focused !== buttonRef.current && focused !== document.body) return;
    const added = first?.querySelector<HTMLElement>("a[href]");
    if (added) added.focus();
    else if (focused === document.body)
      fallbackRef.current?.querySelector<HTMLElement>("a[href]:last-of-type")?.focus();
  });
  const press = () => {
    pending.current = { last: listRef.current?.lastElementChild ?? null, started: false };
  };
  return { listRef, buttonRef, fallbackRef, press };
}
