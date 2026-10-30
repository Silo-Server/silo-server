import { useLayoutEffect, useState, type RefObject } from "react";

/**
 * How far the bottom of the measured section sits below the top of the
 * content under the hero, in pixels, or undefined while it is not shown. A
 * hero given this as fitBelow leaves room for that section on a tall screen.
 */
export function useHeroFitBelow(ref: RefObject<HTMLElement | null>, enabled: boolean) {
  const [fitBelow, setFitBelow] = useState<number>();
  useLayoutEffect(() => {
    const el = ref.current;
    const container = el?.parentElement;
    if (!enabled || !el || !container) return;
    const measure = () => {
      const height = el.getBoundingClientRect().bottom - container.getBoundingClientRect().top;
      setFitBelow(height > 0 ? Math.ceil(height) : undefined);
    };
    // A ResizeObserver reports once when it starts observing, so this also
    // takes the first measurement.
    const observer = new ResizeObserver(measure);
    observer.observe(el);
    return () => observer.disconnect();
  }, [ref, enabled]);
  return enabled ? fitBelow : undefined;
}
