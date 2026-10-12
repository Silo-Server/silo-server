import { useLayoutEffect, type KeyboardEvent, type RefObject } from "react";

/**
 * Roving focus for card rows and grids: only one card's controls are in the
 * Tab order, and arrow keys move between cards. Cards are arbitrary children
 * with several controls each (poster link, play, menu, caption links) that
 * mount lazily, so tab stops are managed on the DOM rather than through props.
 *
 * Each card's wrapper is a direct child of the container carrying
 * `data-roving-index`. The active card keeps its controls' own tabindex; every
 * other card's controls get tabindex -1, which keeps them clickable and
 * readable by screen readers but takes them out of the Tab sequence.
 */
const ROVING_INDEX_ATTR = "data-roving-index";
const SAVED_TABINDEX_ATTR = "data-roving-tabindex";
const FOCUSABLE_SELECTOR = [
  "a[href]",
  "button",
  "input",
  "select",
  "textarea",
  "[tabindex]",
  '[contenteditable="true"]',
].join(",");

function rovingItems(container: HTMLElement): HTMLElement[] {
  return Array.from(container.querySelectorAll<HTMLElement>(`:scope > [${ROVING_INDEX_ATTR}]`));
}

function itemIndex(item: HTMLElement): number {
  return Number(item.getAttribute(ROVING_INDEX_ATTR));
}

/**
 * Whether `el` belongs in the Tab order when its card is the active one.
 * Disabled controls count, so an inactive card's control is already out of
 * the Tab order when an attribute change alone re-enables it (the card menu
 * swaps its disabled placeholder that way).
 */
function isOwnTabStop(el: HTMLElement): boolean {
  return el.hasAttribute(SAVED_TABINDEX_ATTR) || el.tabIndex >= 0;
}

/** The control a card receives focus on: its first enabled tab stop (the poster link). */
export function primaryFocusable(item: Element): HTMLElement | null {
  for (const el of Array.from(item.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR))) {
    if (isOwnTabStop(el) && !el.matches(":disabled")) return el;
  }
  return null;
}

function setItemTabbable(item: HTMLElement, tabbable: boolean) {
  if (tabbable) {
    for (const el of Array.from(item.querySelectorAll<HTMLElement>(`[${SAVED_TABINDEX_ATTR}]`))) {
      const saved = el.getAttribute(SAVED_TABINDEX_ATTR);
      el.removeAttribute(SAVED_TABINDEX_ATTR);
      if (saved) el.setAttribute("tabindex", saved);
      else el.removeAttribute("tabindex");
    }
    return;
  }
  for (const el of Array.from(item.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR))) {
    const current = el.getAttribute("tabindex");
    if (current === "-1") continue;
    // Already demoted, but its component re-rendered a new tabindex over ours.
    if (el.hasAttribute(SAVED_TABINDEX_ATTR)) {
      el.setAttribute(SAVED_TABINDEX_ATTR, current ?? "");
    } else if (isOwnTabStop(el)) {
      el.setAttribute(SAVED_TABINDEX_ATTR, current ?? "");
    } else {
      continue;
    }
    el.setAttribute("tabindex", "-1");
  }
}

/**
 * Leaves only the card at `targetIndex` in the Tab order. When that card is
 * not rendered or has nothing to focus (a placeholder or skeleton), the
 * nearest focusable card after it takes its place, then the nearest before.
 */
function applyRovingTabIndex(container: HTMLElement, targetIndex: number) {
  const items = rovingItems(container);
  let target: HTMLElement | null = null;
  let before: HTMLElement | null = null;
  for (const item of items) {
    if (!primaryFocusable(item)) continue;
    if (itemIndex(item) >= targetIndex) {
      target = item;
      break;
    }
    before = item;
  }
  target ??= before;
  for (const item of items) setItemTabbable(item, item === target);
}

/**
 * Keeps `applyRovingTabIndex` true as cards mount, fill in, or grow controls
 * (menus mount on first hover or focus), and as a control is enabled or
 * re-rendered with a new tabindex. Writes only happen when a value differs,
 * so the observer settles after the pass its own writes trigger.
 */
export function useRovingTabIndex(
  containerRef: RefObject<HTMLElement | null>,
  targetIndex: number,
  enabled = true,
) {
  useLayoutEffect(() => {
    const container = containerRef.current;
    if (!enabled || !container) return;
    const apply = () => applyRovingTabIndex(container, targetIndex);
    apply();
    const observer = new MutationObserver(apply);
    observer.observe(container, {
      childList: true,
      subtree: true,
      attributeFilter: ["disabled", "tabindex"],
    });
    return () => observer.disconnect();
  }, [containerRef, targetIndex, enabled]);
}

/** The roving item `target` sits in, when it is inside `container`'s DOM. */
function closestRovingItem(container: HTMLElement, target: EventTarget | null) {
  if (!(target instanceof Element) || !container.contains(target)) return null;
  const item = target.closest<HTMLElement>(`[${ROVING_INDEX_ATTR}]`);
  return item && item.parentElement === container ? item : null;
}

/** The index a roving item carries, or null when `target` is not in one. */
export function rovingIndexOf(container: HTMLElement, target: EventTarget | null) {
  const item = closestRovingItem(container, target);
  return item ? itemIndex(item) : null;
}

/**
 * The rendered card at `index` and the control it takes focus on, or null
 * when that card is not rendered or has nothing to focus yet.
 */
export function focusableAt(
  container: HTMLElement,
  index: number,
): { item: HTMLElement; control: HTMLElement } | null {
  const item = container.querySelector<HTMLElement>(`:scope > [${ROVING_INDEX_ATTR}="${index}"]`);
  const control = item && primaryFocusable(item);
  return item && control ? { item, control } : null;
}

function isRtl(el: HTMLElement): boolean {
  // Browsers always compute a direction; the dir attribute covers environments
  // that don't (jsdom).
  const direction = getComputedStyle(el).direction || el.closest("[dir]")?.getAttribute("dir");
  return direction === "rtl";
}

export type RovingKey = "prev" | "next" | "up" | "down" | "home" | "end" | "first" | "last";

/**
 * Maps a keydown inside a roving container to a move, or null to let the
 * event through. Left and Right follow reading direction, so they flip in a
 * right-to-left layout. Events from portaled content (an open card menu
 * bubbles through React's tree), editable fields, modified keys, and keys
 * something else already handled are ignored.
 */
export function rovingKeyFor(event: KeyboardEvent, container: HTMLElement): RovingKey | null {
  if (event.defaultPrevented || event.altKey || event.metaKey || event.shiftKey) return null;
  if (event.nativeEvent.isComposing) return null;
  const target = event.target;
  if (!(target instanceof HTMLElement) || !container.contains(target)) return null;
  if (target.isContentEditable || target.matches("input, select, textarea")) return null;
  if (event.ctrlKey) {
    if (event.key === "Home") return "first";
    if (event.key === "End") return "last";
    return null;
  }
  switch (event.key) {
    case "ArrowLeft":
      return isRtl(container) ? "next" : "prev";
    case "ArrowRight":
      return isRtl(container) ? "prev" : "next";
    case "ArrowUp":
      return "up";
    case "ArrowDown":
      return "down";
    case "Home":
      return "home";
    case "End":
      return "end";
    default:
      return null;
  }
}
