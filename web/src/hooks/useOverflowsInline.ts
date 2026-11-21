import { useLayoutEffect, useRef, useState } from "react";

/**
 * Whether an element's text lays out past its content box on either side. Measured from
 * the text's own boxes rather than `scrollWidth`, which also counts an inline element's
 * margin: Firefox lets a trailing margin, such as the gap after a name, hang past the end of
 * a line, so a name that fits would read as overflowing. The clip classes don't move the
 * content box, so turning them on doesn't change the answer. A name that fits can measure a
 * fraction of a pixel wider than its box, so that counts as fitting.
 */
function textOverflows(element: HTMLElement) {
  const range = document.createRange();
  // jsdom, which the tests use, has no layout and no Range.getBoundingClientRect.
  if (typeof range.getBoundingClientRect !== "function") return false;
  range.selectNodeContents(element);
  const text = range.getBoundingClientRect();
  if (text.width === 0) return false;
  const box = element.getBoundingClientRect();
  const style = getComputedStyle(element);
  const start = box.left + parseFloat(style.borderLeftWidth) + parseFloat(style.paddingLeft);
  const end = box.right - parseFloat(style.borderRightWidth) - parseFloat(style.paddingRight);
  return text.right > end + 1 || text.left < start - 1;
}

/**
 * Whether a line-clamped element cuts text that runs the other way from the element it
 * sits in, the only text `data-clamp-cut` restyles (app.css). Text that runs its parent's
 * way skips the layout read. A fraction of a pixel past the clamp counts as fitting.
 */
function clampCutsOppositeText(element: HTMLElement) {
  const parent = element.parentElement;
  if (parent && getComputedStyle(element).direction === getComputedStyle(parent).direction) {
    return false;
  }
  return element.scrollHeight > element.clientHeight + 1;
}

/**
 * `measure` for the element behind the returned ref: after layout, and again whenever the
 * box resizes, a web font finishes loading, the text weight preference or the interface
 * language changes, or `content` changes. Fonts, weight and language can all reflow text
 * without resizing its box, so the resize observer alone would miss them; the language
 * also changes how text such as Japanese breaks into lines.
 */
function useMeasuredFlag<T extends HTMLElement>(
  measure: (element: T) => boolean,
  content: unknown,
) {
  const ref = useRef<T>(null);
  const [flag, setFlag] = useState(false);
  useLayoutEffect(() => {
    const element = ref.current;
    if (!element) return;
    let active = true;
    const update = () => {
      if (active) setFlag(measure(element));
    };
    update();
    // `ready` covers a font load already under way (WebKit fires no "loadingdone"); the
    // event covers later ones. With nothing loading, the first measure already counts.
    const fonts = "fonts" in document ? document.fonts : undefined;
    if (fonts?.status === "loading") void fonts.ready.then(update);
    fonts?.addEventListener("loadingdone", update);
    const typography = new MutationObserver(update);
    typography.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ["data-text-weight", "lang"],
    });
    const observer = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(update);
    observer?.observe(element);
    return () => {
      active = false;
      observer?.disconnect();
      typography.disconnect();
      fonts?.removeEventListener("loadingdone", update);
    };
  }, [measure, content]);
  return [ref, flag] as const;
}

/**
 * Whether an element's text is wider than its box, such as a name or folder path
 * holding one long run of joined emoji that no line break can split. Clip such
 * text sideways only while this is true: clipping text that fits would trim letters
 * whose ink reaches past their edge, and in Firefox also tall marks above and
 * below the line. Re-measured as `useMeasuredFlag` describes.
 */
export function useOverflowsInline<T extends HTMLElement>(content: unknown) {
  return useMeasuredFlag<T>(textOverflows, content);
}

/**
 * Whether a line-clamped `.bidi-text` element cuts text that runs the other way from the
 * element it sits in, for its `data-clamp-cut` (see app.css). Re-measured as
 * `useMeasuredFlag` describes.
 */
export function useClampCut<T extends HTMLElement>(content: unknown) {
  return useMeasuredFlag<T>(clampCutsOppositeText, content);
}

/**
 * The classes for text while `useOverflowsInline` says it overflows, applied through `cn` to
 * a block or flex-item box (a clip does nothing on an inline one). It clips sideways at the
 * box's end edge. The start padding, offset by a negative margin, keeps a first letter whole
 * whose ink reaches before its box, such as a leading "j"; the wider max-width lets that room
 * fit inside a box otherwise capped at `max-w-full`, so the cut stays flush with its
 * container.
 */
export const OVERFLOW_CLIP = "-ms-[0.25em] max-w-[calc(100%+0.25em)] overflow-x-clip ps-[0.25em]";
