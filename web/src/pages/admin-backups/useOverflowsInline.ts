import { useLayoutEffect, useRef, useState } from "react";

/**
 * Whether an element's text is wider than its box, such as a name holding one
 * long run of joined emoji that no line break can split. A row clips such text
 * sideways only while this is true: clipping text that fits would trim letters
 * whose ink reaches past their edge, and in Firefox also tall marks above and
 * below the line. Measured after layout, again whenever the box resizes, and
 * whenever `content` changes. A name that fits can measure a fraction of a pixel
 * wider than its box, so that counts as fitting.
 */
export function useOverflowsInline<T extends HTMLElement>(content: unknown) {
  const ref = useRef<T>(null);
  const [overflows, setOverflows] = useState(false);
  useLayoutEffect(() => {
    const element = ref.current;
    if (!element) return;
    const measure = () => setOverflows(element.scrollWidth > element.clientWidth + 1);
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(element);
    return () => observer.disconnect();
  }, [content]);
  return [ref, overflows] as const;
}
