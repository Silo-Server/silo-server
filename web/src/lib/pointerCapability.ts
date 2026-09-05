// Whether the app may reveal hover-only affordances, published as
// `data-fine-pointer` on <html> for CSS to gate on.
//
// The hover/pointer media features are the obvious way to ask this, and they
// are wrong on some Windows hybrid machines: Chromium there reports
// `any-hover: none` and `any-pointer: coarse` even while a mouse is attached
// and actively hovering, because it has enumerated the touch digitizer and
// nothing else. The `any-*` features exist precisely to describe *any*
// attached device, so there is nothing more permissive left to widen to — the
// answer itself is wrong, and a media query cannot recover from it. Users on
// those machines are left with controls stuck at `opacity: 0`.
//
// So observe rather than ask. A `pointermove` carrying `pointerType: "mouse"`
// is ground truth: whatever the OS claims, a mouse is being used. The media
// query still seeds the initial value — it is correct on the vast majority of
// devices, and it is the only answer available before the first pointer event
// — and observed events override it from there.
//
// Last pointer wins in both directions. A touch press clears the flag again so
// tap-emulated `:hover` on a hybrid cannot uncover the controls and strand
// them visible, which is what the media-query gate was protecting against.

const FINE_POINTER_ATTR = "data-fine-pointer";
const FINE_POINTER_QUERY = "(any-hover: hover) and (any-pointer: fine)";

// Components decide what to mount from this too, not just CSS, so the value has
// to be subscribable rather than only readable off the DOM.
const listeners = new Set<() => void>();

/**
 * The published capability, or undefined when nothing has published one —
 * server rendering, tests, and any browser where init has not run. Callers keep
 * their own established default in that case rather than being told "coarse".
 *
 * The document is resolved inside the function rather than as a default
 * parameter: a default is evaluated at the call site, so `readFinePointer()`
 * where no document exists would throw before it could answer undefined,
 * contradicting the line above.
 */
export function readFinePointer(doc?: Document): boolean | undefined {
  const target = doc ?? (typeof document === "undefined" ? undefined : document);
  if (target === undefined) return undefined;
  const value = target.documentElement.getAttribute(FINE_POINTER_ATTR);
  return value === null ? undefined : value === "true";
}

export function subscribeFinePointer(onChange: () => void): () => void {
  listeners.add(onChange);
  return () => {
    listeners.delete(onChange);
  };
}

interface PointerCapabilityDeps {
  doc: Document;
  win: Window;
}

export function initPointerCapability(deps?: Partial<PointerCapabilityDeps>): () => void {
  const doc = deps?.doc ?? document;
  const win = deps?.win ?? window;
  const root = doc.documentElement;

  const set = (fine: boolean) => {
    const next = fine ? "true" : "false";
    if (root.getAttribute(FINE_POINTER_ATTR) === next) return;
    root.setAttribute(FINE_POINTER_ATTR, next);
    listeners.forEach((listener) => listener());
  };

  // Once a real pointer has been seen, the media query stops being evidence:
  // it is the signal already known to be wrong here, and letting a later
  // change event promote would overwrite an observed touch and re-enable
  // tap-emulated hover on a hybrid.
  let observedPointer = false;

  // Seeded synchronously, before first paint, so a mouse-only desktop never
  // flashes its hover controls hidden while waiting for a pointer event.
  // jsdom has no matchMedia, hence the optional call rather than a stub.
  const query = win.matchMedia?.(FINE_POINTER_QUERY);
  set(query?.matches ?? false);

  // Only ever promotes, and only until a real pointer is seen. A query that
  // flips to `false` says a device was detached, which tells us nothing about
  // the pointer in the user's hand.
  const onQueryChange = (event: MediaQueryListEvent) => {
    if (!observedPointer && event.matches) set(true);
  };
  query?.addEventListener?.("change", onQueryChange);

  const onPointer = (event: PointerEvent) => {
    if (event.pointerType === "mouse" || event.pointerType === "pen") {
      observedPointer = true;
      set(true);
    } else if (event.pointerType === "touch") {
      observedPointer = true;
      set(false);
    }
  };

  // `pointerdown` as well as `pointermove` so a mouse that clicks without
  // moving is still seen, and so a touch press clears the flag before
  // Chromium's emulated `:hover` lands on the tapped card.
  //
  // Capture phase: a `stopPropagation()` anywhere in the tree would otherwise
  // blind this, and the app cannot know which handler that might be.
  const opts = { passive: true, capture: true } as const;
  doc.addEventListener("pointermove", onPointer, opts);
  doc.addEventListener("pointerdown", onPointer, opts);

  return () => {
    query?.removeEventListener?.("change", onQueryChange);
    doc.removeEventListener("pointermove", onPointer, opts);
    doc.removeEventListener("pointerdown", onPointer, opts);
  };
}
