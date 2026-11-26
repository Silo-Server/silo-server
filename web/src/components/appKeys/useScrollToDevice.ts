import { useEffect } from "react";
import { useLocation } from "react-router";
import { motionScrollBehavior } from "@/hooks/usePrefersReducedMotion";

/**
 * Scrolls to the device a link names (`#device-<id>`) or to the devices list
 * (`#devices`) once the list has rendered, since the router doesn't. A device
 * that isn't listed (removed since, or this session can't list devices)
 * falls back to the list.
 */
export function useScrollToDevice(ready: boolean) {
  const { hash } = useLocation();
  useEffect(() => {
    if (!ready || (hash !== "#devices" && !hash.startsWith("#device-"))) return;
    const target =
      document.getElementById(decodeURIComponent(hash.slice(1))) ??
      document.getElementById("devices");
    target?.scrollIntoView?.({ block: "start", behavior: motionScrollBehavior() });
  }, [hash, ready]);
}
