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
 * Whether an element's text is wider than its box, such as a name or folder path
 * holding one long run of joined emoji that no line break can split. Clip such
 * text sideways only while this is true: clipping text that fits would trim letters
 * whose ink reaches past their edge, and in Firefox also tall marks above and
 * below the line. Measured after layout, again whenever the box resizes or a
 * web font finishes loading, and whenever `content` changes.
 */
export function useOverflowsInline<T extends HTMLElement>(content: unknown) {
  const ref = useRef<T>(null);
  const [overflows, setOverflows] = useState(false);
  useLayoutEffect(() => {
    const element = ref.current;
    if (!element) return;
    let active = true;
    const measure = () => {
      if (active) setOverflows(textOverflows(element));
    };
    measure();
    // A web font that finishes loading after the first measure changes the text's width
    // without resizing its box, so the resize observer alone would miss it. `ready` covers
    // a load already under way (WebKit fires no "loadingdone"); the event covers later ones.
    const fonts = "fonts" in document ? document.fonts : undefined;
    void fonts?.ready.then(measure);
    fonts?.addEventListener("loadingdone", measure);
    const observer = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(measure);
    observer?.observe(element);
    return () => {
      active = false;
      observer?.disconnect();
      fonts?.removeEventListener("loadingdone", measure);
    };
  }, [content]);
  return [ref, overflows] as const;
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

/** `OVERFLOW_CLIP` for a narrow row only (below 42rem); a wider row truncates instead. */
export const NARROW_OVERFLOW_CLIP =
  "@max-2xl:-ms-[0.25em] @max-2xl:max-w-[calc(100%+0.25em)] @max-2xl:overflow-x-clip @max-2xl:ps-[0.25em]";
