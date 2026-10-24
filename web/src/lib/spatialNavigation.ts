// Arrow-key focus movement for TV remotes and D-pads.
//
// An HTPC remote sends arrows, Enter and Back, and nothing else. Tab order
// cannot reach the card two rows down, so the arrows move focus to the nearest
// focusable element in the pressed direction, measured on screen. The engine
// works over the elements that are already focusable; components opt into
// nothing except, for rows, `data-nav-group` (see below).
//
// Components keep every arrow they already own. The listener sits on window,
// so it runs after React handlers and every document listener, and it acts
// only on events none of them prevented: menus, sliders, tab lists and the
// player claim their arrows first and this never sees them.
//
// `data-nav-group` marks a row. Entering a row from outside returns to the
// card last focused in it, the way TV apps do, instead of whichever card
// happens to sit nearest the element being left.
//
// `data-nav-skip` keeps a control out of arrow movement when another stop
// beside it already leads to the same place.
//
// `data-nav-scope` confines movement to a surface drawn over the page (the
// video player), the same way an open modal dialog does.

export type NavDirection = "up" | "down" | "left" | "right";

export interface NavBox {
  left: number;
  top: number;
  right: number;
  bottom: number;
}

const DIRECTIONS: Record<string, NavDirection> = {
  ArrowUp: "up",
  ArrowDown: "down",
  ArrowLeft: "left",
  ArrowRight: "right",
};

const FOCUSABLE =
  'a[href], button, input, select, textarea, summary, [tabindex], [contenteditable="true"]';
const DIALOG = '[role="dialog"][aria-modal="true"], [role="alertdialog"]';
const SCOPE = `${DIALOG}, [data-nav-scope]`;
const GROUP = "[data-nav-group]";
const LIST_ITEM = 'li, [role="listitem"]';

// Rings, negative margins and rounding let neighbours overlap by a few pixels
// without either being "behind" the other.
const OVERLAP_TOLERANCE = 8;
// Drifting sideways costs more than travelling forward, so Down from a card
// picks the card below it over a nearer one off to the side.
const ORTHOGONAL_WEIGHT = 2;

function intervalGap(aStart: number, aEnd: number, bStart: number, bEnd: number): number {
  return Math.max(0, bStart - aEnd, aStart - bEnd);
}

function intervalOverlap(aStart: number, aEnd: number, bStart: number, bEnd: number): number {
  return Math.max(0, Math.min(aEnd, bEnd) - Math.max(aStart, bStart));
}

/**
 * How far `to` lies from `from` in `direction`, or null when it is not in that
 * direction at all. Compared in order, lower is nearer:
 *
 * 1. Whether `to` is out of line, sharing none of `from`'s cross axis. Anything
 *    in line wins, so Up from a card reaches the card above it rather than a
 *    sidebar link off to the side that happens to be closer.
 * 2. Distance, where sharing more of the cross axis counts in a candidate's
 *    favour: the card beside this one beats a short sidebar link the same
 *    distance away.
 * 3. Alignment of centres, for exact ties.
 *
 * With `cone` false, an out-of-line box counts however far off to the side it
 * lies.
 */
export function directionalDistance(
  from: NavBox,
  to: NavBox,
  direction: NavDirection,
  cone = true,
): [number, number, number] | null {
  const horizontal = direction === "left" || direction === "right";
  const fromCenter = horizontal ? (from.left + from.right) / 2 : (from.top + from.bottom) / 2;
  const toCenter = horizontal ? (to.left + to.right) / 2 : (to.top + to.bottom) / 2;
  let gap: number;
  switch (direction) {
    case "right":
      gap = to.left - from.right;
      break;
    case "left":
      gap = from.left - to.right;
      break;
    case "down":
      gap = to.top - from.bottom;
      break;
    case "up":
      gap = from.top - to.bottom;
      break;
  }
  const forward = direction === "right" || direction === "down" ? 1 : -1;
  if (gap < -OVERLAP_TOLERANCE || (toCenter - fromCenter) * forward <= 0) return null;

  const [fromStart, fromEnd, toStart, toEnd] = horizontal
    ? [from.top, from.bottom, to.top, to.bottom]
    : [from.left, from.right, to.left, to.right];
  const overlap = intervalOverlap(fromStart, fromEnd, toStart, toEnd);
  const sideways = intervalGap(fromStart, fromEnd, toStart, toEnd);
  // Out of line, it must still lie within 45° of the direction pressed: Down
  // from the corner of a page is not a way to reach the far side of the screen.
  if (cone && overlap === 0 && sideways > Math.max(0, gap)) return null;
  const distance = Math.max(0, gap) + sideways * ORTHOGONAL_WEIGHT - Math.sqrt(overlap);
  const centerOffset = Math.abs((fromStart + fromEnd) / 2 - (toStart + toEnd) / 2);
  return [overlap > 0 ? 0 : 1, distance, centerOffset];
}

function isNearer(a: readonly number[], b: readonly number[]): boolean {
  for (let i = 0; i < a.length; i++) {
    if (a[i] !== b[i]) return (a[i] ?? 0) < (b[i] ?? 0);
  }
  return false;
}

/** Index of the nearest box in `direction`, or -1 when there is none. */
export function pickNearest(
  from: NavBox,
  boxes: NavBox[],
  direction: NavDirection,
  cone = true,
): number {
  let best = -1;
  let bestScore: number[] | null = null;
  boxes.forEach((box, index) => {
    const score = directionalDistance(from, box, direction, cone);
    if (score && (!bestScore || isNearer(score, bestScore))) {
      best = index;
      bestScore = score;
    }
  });
  return best;
}

function isTextEntry(el: Element): boolean {
  if (el instanceof HTMLTextAreaElement || el instanceof HTMLSelectElement) return true;
  if (el instanceof HTMLElement && el.isContentEditable) return true;
  return false;
}

/**
 * Whether the focused element uses this arrow itself. A text field keeps Left
 * and Right for its caret but hands Up and Down on, so a remote can leave the
 * search box for the results below it.
 */
function ownsArrow(el: Element, direction: NavDirection): boolean {
  if (isTextEntry(el)) return true;
  if (el instanceof HTMLMediaElement) return true;
  if (el instanceof HTMLInputElement) {
    if (el.type === "radio" || el.type === "range") return true;
    return direction === "left" || direction === "right";
  }
  return false;
}

function isVisible(el: HTMLElement): boolean {
  if (
    typeof el.checkVisibility === "function" &&
    !el.checkVisibility({ opacityProperty: true, visibilityProperty: true })
  ) {
    return false;
  }
  // Visually hidden controls (a skip link) measure one pixel until focused.
  const rect = el.getBoundingClientRect();
  return rect.width > 1 && rect.height > 1;
}

function isCandidate(el: HTMLElement): boolean {
  if (el.tabIndex < 0 || el.matches(":disabled") || el.hasAttribute("data-nav-skip")) return false;
  if (el.closest('[inert], [aria-hidden="true"]')) return false;
  return isVisible(el);
}

function collectCandidates(scope: Element): HTMLElement[] {
  const found = Array.from(scope.querySelectorAll<HTMLElement>(FOCUSABLE)).filter(isCandidate);
  // A focusable container (a scrollable row given tabindex for keyboard
  // scrolling) is not itself a stop when it holds stops of its own; it would
  // otherwise win every move into its row.
  const set = new Set(found);
  const containers = new Set<HTMLElement>();
  for (const el of found) {
    for (let parent = el.parentElement; parent && parent !== scope; parent = parent.parentElement) {
      if (set.has(parent)) containers.add(parent);
    }
  }
  // A card links its poster, title and year to the same page. Those are one
  // stop, the first (the poster), or Down would take three presses per row.
  const hrefsByItem = new Map<Element, Set<string>>();
  return found.filter((el) => {
    if (
      containers.has(el) &&
      !(el instanceof HTMLAnchorElement || el instanceof HTMLButtonElement)
    ) {
      return false;
    }
    if (!(el instanceof HTMLAnchorElement)) return true;
    const item = el.closest(LIST_ITEM);
    if (!item) return true;
    const hrefs = hrefsByItem.get(item) ?? new Set<string>();
    hrefsByItem.set(item, hrefs);
    if (hrefs.has(el.href)) return false;
    hrefs.add(el.href);
    return true;
  });
}

/**
 * A row scrolls its cards out of sight rather than off the page. Those cards
 * are stops only from inside the row, where moving onto one scrolls it in;
 * from outside, one hidden off to the left would otherwise look like the
 * nearest thing left of the sidebar.
 */
function reachableFrom(focusedGroup: Element | null, el: HTMLElement): boolean {
  const group = el.closest(GROUP);
  if (!group || group === focusedGroup) return true;
  const box = el.getBoundingClientRect();
  const visible = group.getBoundingClientRect();
  return (
    intervalOverlap(box.left, box.right, visible.left, visible.right) > 0 &&
    intervalOverlap(box.top, box.bottom, visible.top, visible.bottom) > 0
  );
}

function activeScope(active: Element | null): Element {
  const own = active?.closest(SCOPE);
  if (own) return own;
  const open = document.querySelectorAll(SCOPE);
  return open[open.length - 1] ?? document.body;
}

/**
 * Where focus enters a page: the page's own starting point (`data-nav-default`,
 * such as a title's Play button) when one is on screen, otherwise the first
 * card of the page's rows, otherwise the first stop reading top to bottom,
 * preferring the page over its chrome.
 */
function entryCandidate(candidates: HTMLElement[]): HTMLElement | null {
  const onScreen = candidates.filter((el) => {
    const rect = el.getBoundingClientRect();
    return (
      rect.bottom > 0 &&
      rect.top < window.innerHeight &&
      rect.right > 0 &&
      rect.left < window.innerWidth
    );
  });
  const preferred = onScreen.find((el) => el.closest("[data-nav-default]"));
  if (preferred) return preferred;
  const main = document.querySelector("main");
  const inMain = main ? onScreen.filter((el) => main.contains(el)) : [];
  let pool = inMain.filter((el) => el.closest(GROUP));
  if (pool.length === 0) pool = inMain;
  if (pool.length === 0) pool = onScreen;
  let best: HTMLElement | null = null;
  let bestRect: DOMRect | null = null;
  for (const el of pool) {
    const rect = el.getBoundingClientRect();
    if (
      !bestRect ||
      rect.top < bestRect.top ||
      (rect.top === bestRect.top && rect.left < bestRect.left)
    ) {
      best = el;
      bestRect = rect;
    }
  }
  return best;
}

// Focus scrolling stops as soon as an element is inside the viewport, which
// leaves a row flush with the top edge and its heading out of sight.
const EDGE_MARGIN = 96;

function keepClearOfEdges(el: HTMLElement) {
  // Only the page scrolls this way; the sidebar, dialogs and the player do not.
  if (!document.querySelector("main")?.contains(el)) return;
  const rect = el.getBoundingClientRect();
  const room = window.innerHeight - EDGE_MARGIN * 2;
  if (rect.height > room) return;
  if (rect.top < EDGE_MARGIN) {
    window.scrollBy({ top: rect.top - EDGE_MARGIN });
  } else if (rect.bottom > window.innerHeight - EDGE_MARGIN) {
    window.scrollBy({ top: rect.bottom - (window.innerHeight - EDGE_MARGIN) });
  }
}

function isModified(event: KeyboardEvent): boolean {
  return event.altKey || event.ctrlKey || event.metaKey || event.shiftKey;
}

/**
 * Installs arrow-key navigation on `window` and returns its uninstaller.
 *
 * Arrows with nothing focused move focus into the page, unless a mouse or
 * wheel was the last thing used: then the page scrolls as it always has, and
 * a desktop reader is never pulled onto a link they did not ask for.
 */
export function installSpatialNavigation(): () => void {
  const lastInGroup = new WeakMap<Element, HTMLElement>();
  let pointerLast = false;

  const onPointer = () => {
    pointerLast = true;
  };
  const onFocusIn = (event: FocusEvent) => {
    const target = event.target;
    if (!(target instanceof HTMLElement)) return;
    const group = target.closest(GROUP);
    if (group) lastInGroup.set(group, target);
  };
  const onKeyDown = (event: KeyboardEvent) => {
    const direction = DIRECTIONS[event.key];
    if (!direction || event.defaultPrevented || isModified(event)) return;

    const active = document.activeElement;
    const focused = active instanceof HTMLElement && active !== document.body ? active : null;
    if (focused && ownsArrow(focused, direction)) return;
    if (!focused && pointerLast) return;

    const scope = activeScope(focused);
    const candidates = collectCandidates(scope);
    let next: HTMLElement | null;
    // Move from whatever holds focus, even a container that is not a stop
    // itself; only with nothing visible focused does the page take focus.
    if (!focused || !isVisible(focused)) {
      next = entryCandidate(candidates);
    } else {
      const focusedGroup = focused.closest(GROUP);
      const others = candidates.filter((el) => el !== focused && reachableFrom(focusedGroup, el));
      const from = focused.getBoundingClientRect();
      const boxes = others.map((el) => el.getBoundingClientRect());
      let index = pickNearest(from, boxes, direction);
      // A dialog has no far side of the screen to jump to, so rather than a
      // dead end, take the nearest control that way however far off line.
      if (index < 0 && scope.matches(DIALOG)) index = pickNearest(from, boxes, direction, false);
      next = others[index] ?? null;
      const group = next?.closest(GROUP);
      if (group && group !== focusedGroup) {
        const remembered = lastInGroup.get(group);
        const current = group.querySelector<HTMLElement>('[aria-current="page"]');
        if (remembered && others.includes(remembered)) next = remembered;
        else if (current && others.includes(current)) next = current;
      }
    }
    // Nothing that way: let the arrow scroll, which also brings a virtualized
    // list's next rows into the DOM for the following press.
    if (!next) return;

    event.preventDefault();
    pointerLast = false;
    next.focus();
    keepClearOfEdges(next);
  };

  window.addEventListener("keydown", onKeyDown);
  document.addEventListener("focusin", onFocusIn);
  document.addEventListener("pointerdown", onPointer, true);
  document.addEventListener("wheel", onPointer, { capture: true, passive: true });
  return () => {
    window.removeEventListener("keydown", onKeyDown);
    document.removeEventListener("focusin", onFocusIn);
    document.removeEventListener("pointerdown", onPointer, true);
    document.removeEventListener("wheel", onPointer, { capture: true });
  };
}
