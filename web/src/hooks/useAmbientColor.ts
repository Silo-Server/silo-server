import { useEffect, useRef } from "react";
import { getAverageColor } from "@/lib/thumbhash";

const PROPERTY = "--ambient";
let currentOwner: symbol | null = null;

/**
 * Sets the --ambient CSS custom property on <html> from a thumbhash.
 *
 * Ambient surfaces can replace one another in the same route commit. Cleanup
 * is deferred to a microtask so the outgoing surface cannot briefly clear the
 * property before the incoming surface publishes its color. That avoids a
 * full-page gradient repaint between item-detail routes while still restoring
 * the CSS fallback after the final ambient surface unmounts.
 */
export function useAmbientColor(thumbhash: string | undefined | null): void {
  const prevRef = useRef<string | null>(null);

  useEffect(() => {
    const root = document.documentElement;
    const owner = Symbol("ambient-color");

    if (!thumbhash) {
      return;
    }

    const color = getAverageColor(thumbhash);
    if (!color) {
      return;
    }

    currentOwner = owner;

    // Only write to DOM if value changed
    if (color !== prevRef.current || root.style.getPropertyValue(PROPERTY) !== color) {
      root.style.setProperty(PROPERTY, color);
      prevRef.current = color;
    }

    return () => {
      queueMicrotask(() => {
        if (currentOwner !== owner) return;
        root.style.removeProperty(PROPERTY);
        currentOwner = null;
        prevRef.current = null;
      });
    };
  }, [thumbhash]);
}
