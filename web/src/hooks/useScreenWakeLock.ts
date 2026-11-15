import { useEffect, useRef } from "react";

export function useScreenWakeLock(active: boolean) {
  const sentinelRef = useRef<WakeLockSentinel | null>(null);

  useEffect(() => {
    if (!active) {
      sentinelRef.current?.release().catch(() => {});
      sentinelRef.current = null;
      return;
    }
    if (!("wakeLock" in navigator)) return;

    let cancelled = false;
    const acquire = async () => {
      try {
        const sentinel = await (
          navigator as Navigator & {
            wakeLock: { request: (type: "screen") => Promise<WakeLockSentinel> };
          }
        ).wakeLock.request("screen");
        if (cancelled) {
          await sentinel.release().catch(() => {});
          return;
        }
        sentinelRef.current = sentinel;
        // The browser releases the lock whenever the page is hidden. Forget it
        // then, so the visibility handler requests a new one on return. If the
        // release arrives after the page is visible again, request one here.
        sentinel.addEventListener("release", () => {
          if (sentinelRef.current !== sentinel) return;
          sentinelRef.current = null;
          if (!cancelled && document.visibilityState === "visible") void acquire();
        });
      } catch {
        // Unsupported or denied; reading still works without it.
      }
    };
    void acquire();

    const onVisible = () => {
      if (document.visibilityState === "visible" && !sentinelRef.current) {
        void acquire();
      }
    };
    document.addEventListener("visibilitychange", onVisible);

    return () => {
      cancelled = true;
      document.removeEventListener("visibilitychange", onVisible);
      sentinelRef.current?.release().catch(() => {});
      sentinelRef.current = null;
    };
  }, [active]);
}

interface WakeLockSentinel {
  release(): Promise<void>;
  addEventListener(type: "release", listener: () => void): void;
}
