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
    if (root.getAttribute(FINE_POINTER_ATTR) !== next) root.setAttribute(FINE_POINTER_ATTR, next);
  };

  // Seeded synchronously, before first paint, so a mouse-only desktop never
  // flashes its hover controls hidden while waiting for a pointer event.
  // jsdom has no matchMedia, hence the optional call rather than a stub.
  const query = win.matchMedia?.(FINE_POINTER_QUERY);
  set(query?.matches ?? false);

  // Only ever promotes. A media query that flips to `false` says a device was
  // detached, which tells us nothing about the pointer in the user's hand, and
  // on the machines this exists for it is the untrustworthy signal to begin
  // with.
  const onQueryChange = (event: MediaQueryListEvent) => {
    if (event.matches) set(true);
  };
  query?.addEventListener?.("change", onQueryChange);

  const onPointer = (event: PointerEvent) => {
    if (event.pointerType === "mouse" || event.pointerType === "pen") set(true);
    else if (event.pointerType === "touch") set(false);
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
