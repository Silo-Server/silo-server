import { useCallback, useRef } from "react";

interface UseIntersectionObserverOptions {
  onIntersect: () => void;
  enabled: boolean;
  rootMargin?: string;
  threshold?: number;
}

export function useIntersectionObserver({
  onIntersect,
  enabled,
  rootMargin = "600px",
  threshold = 0,
}: UseIntersectionObserverOptions) {
  const callbackRef = useRef(onIntersect);
  callbackRef.current = onIntersect;
  const observerRef = useRef<IntersectionObserver | null>(null);

  const sentinelRef = useCallback(
    (node: HTMLDivElement | null) => {
      // Disconnect any previous observer
      if (observerRef.current) {
        observerRef.current.disconnect();
        observerRef.current = null;
      }

      // Environments without the API (server rendering, jsdom) get no
      // observer; callers keep an explicit control for that case.
      if (!node || !enabled || typeof IntersectionObserver === "undefined") return;

      observerRef.current = new IntersectionObserver(
        (entries) => {
          if (entries[0]?.isIntersecting) {
            callbackRef.current();
          }
        },
        { rootMargin, threshold },
      );

      observerRef.current.observe(node);
    },
    [enabled, rootMargin, threshold],
  );

  return sentinelRef;
}
