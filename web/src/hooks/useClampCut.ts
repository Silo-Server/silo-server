import { useLayoutEffect, useRef } from "react";

// Only browsers with scroll-state support apply the data-clamp-cut rule (app.css), so
// elsewhere there's nothing to measure.
const STYLED =
  typeof CSS !== "undefined" &&
  typeof CSS.supports === "function" &&
  CSS.supports("container-type", "scroll-state");

/**
 * Whether a line-clamped element cuts text that runs the other way from the element it
 * sits in, the only text the rule restyles. Text that runs its parent's way skips the
 * layout read. A fraction of a pixel past the clamp counts as fitting.
 */
function cutsOppositeText(element: HTMLElement) {
  const parent = element.parentElement;
  if (parent && getComputedStyle(element).direction === getComputedStyle(parent).direction) {
    return false;
  }
  return element.scrollHeight > element.clientHeight + 1;
}

// Every clamped element shares one set of observers, and each pass reads every element
// before it writes any attribute. Measuring and marking element by element would make
// each read lay out the page again after the previous element's write; a season with
// hundreds of episode cards then takes seconds to show.
const tracked = new Set<HTMLElement>();
let pending: Set<HTMLElement> | null = null;
let resize: ResizeObserver | null = null;
let typography: MutationObserver | null = null;
let awaitingFonts = false;

function flush() {
  const elements = [...(pending ?? [])].filter((element) => tracked.has(element));
  pending = null;
  const cuts = elements.map(cutsOppositeText);
  elements.forEach((element, index) => element.toggleAttribute("data-clamp-cut", cuts[index]));
}

// The pass runs in a microtask: after the render or observer callback that asked for it,
// before the browser paints, so the attribute changes in the frame that changed the cut.
function schedule(element: HTMLElement) {
  if (!pending) {
    pending = new Set();
    queueMicrotask(flush);
  }
  pending.add(element);
}

function scheduleAll() {
  tracked.forEach(schedule);
}

function track(element: HTMLElement) {
  if (tracked.size === 0) {
    resize =
      typeof ResizeObserver === "undefined"
        ? null
        : new ResizeObserver((entries) => {
            for (const entry of entries) schedule(entry.target as HTMLElement);
          });
    // A web font, the text weight preference and the interface language can all reflow
    // text without resizing its box. The language also changes how text such as Japanese
    // breaks into lines, and the direction which text runs the other way.
    typography = new MutationObserver(scheduleAll);
    typography.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ["data-text-weight", "lang", "dir"],
    });
    document.fonts?.addEventListener("loadingdone", scheduleAll);
  }
  tracked.add(element);
  resize?.observe(element);
  // `ready` covers a font load already under way (WebKit fires no "loadingdone"). One
  // wait serves every element tracked while it lasts.
  if (!awaitingFonts && document.fonts?.status === "loading") {
    awaitingFonts = true;
    void document.fonts.ready.then(() => {
      awaitingFonts = false;
      scheduleAll();
    });
  }
  schedule(element);
}

function untrack(element: HTMLElement) {
  tracked.delete(element);
  resize?.unobserve(element);
  if (tracked.size > 0) return;
  resize?.disconnect();
  typography?.disconnect();
  document.fonts?.removeEventListener("loadingdone", scheduleAll);
  resize = null;
  typography = null;
}

/**
 * The ref for a line-clamped `.bidi-text` element. While the clamp cuts text that runs the
 * other way from the element it sits in, the element carries `data-clamp-cut`, which
 * aligns it to its own start in Chromium so its "…" shows (app.css). Re-measured whenever
 * the box resizes, a web font loads, the text weight, language or direction changes, or
 * `content` changes.
 */
export function useClampCut<T extends HTMLElement>(content: unknown) {
  const ref = useRef<T>(null);
  useLayoutEffect(() => {
    const element = ref.current;
    if (!STYLED || !element) return;
    track(element);
    return () => untrack(element);
  }, [content]);
  return ref;
}
