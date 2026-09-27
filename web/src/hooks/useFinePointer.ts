import { useCallback, useSyncExternalStore } from "react";
import { readFinePointer, subscribeFinePointer } from "@/lib/pointerCapability";

/**
 * Whether the pointer in use is a precise, hover-capable one, as observed by
 * `lib/pointerCapability.ts`.
 *
 * Prefer this over reading `(any-hover: hover) and (any-pointer: fine)`
 * directly: Chromium on some Windows machines with a touchscreen answers that
 * query `false` while a mouse is actively in use, so a component gating on it
 * withholds its hover affordances on exactly the devices that can use them.
 *
 * `fallback` answers where no capability has been published — server
 * rendering, tests, and browsers where init has not run — so a caller can keep
 * whatever default it had before.
 */
export function useFinePointer(fallback = false): boolean {
  const getSnapshot = useCallback(() => readFinePointer() ?? fallback, [fallback]);
  const getServerSnapshot = useCallback(() => fallback, [fallback]);
  return useSyncExternalStore(subscribeFinePointer, getSnapshot, getServerSnapshot);
}
