import { useCallback, useEffect, useRef, useState } from "react";

import { api } from "@/api/client";
import { cacheVapidKey, pushSupported, urlBase64ToUint8Array } from "@/lib/push";

export type PushDeviceStatus = "unsupported" | "blocked" | "off" | "on" | "pending";

export function usePushDevice() {
  const [status, setStatus] = useState<PushDeviceStatus>("pending");
  // Generation counter: incremented by enable()/disable() so that any concurrent
  // refresh() can detect it has been superseded and must not overwrite the result.
  const genRef = useRef(0);

  const refresh = useCallback(async () => {
    const myGen = genRef.current;
    if (!pushSupported()) {
      setStatus("unsupported");
      return;
    }
    if (Notification.permission === "denied") {
      setStatus("blocked");
      return;
    }
    try {
      const reg = await navigator.serviceWorker.ready;
      const sub = await reg.pushManager.getSubscription();
      // Only apply if no enable()/disable() started after us.
      if (genRef.current === myGen) {
        setStatus(sub ? "on" : "off");
      }
    } catch {
      if (genRef.current === myGen) {
        setStatus("off");
      }
    }
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const enable = useCallback(async () => {
    if (!pushSupported()) {
      setStatus("unsupported");
      return;
    }
    const myGen = (genRef.current += 1);
    const set = (s: PushDeviceStatus) => {
      if (genRef.current === myGen) setStatus(s);
    };
    set("pending");
    try {
      const perm = await Notification.requestPermission();
      if (perm !== "granted") {
        set(perm === "denied" ? "blocked" : "off");
        return;
      }
      const reg = await navigator.serviceWorker.ready;
      const { vapid_public_key } = await api<{ vapid_public_key: string }>(
        "/notifications/push/webpush-key",
      );
      if (!vapid_public_key) {
        set("off");
        return;
      }
      await cacheVapidKey(vapid_public_key);
      const sub = await reg.pushManager.subscribe({
        userVisibleOnly: true,
        applicationServerKey: urlBase64ToUint8Array(vapid_public_key) as BufferSource,
      });
      await api("/notifications/push/device", {
        method: "PUT",
        body: JSON.stringify({ transport: "webpush", token: JSON.stringify(sub.toJSON()) }),
      });
      set("on");
    } catch {
      set("off");
    }
  }, []);

  const disable = useCallback(async () => {
    const myGen = (genRef.current += 1);
    const set = (s: PushDeviceStatus) => {
      if (genRef.current === myGen) setStatus(s);
    };
    set("pending");
    try {
      const reg = await navigator.serviceWorker.ready;
      const sub = await reg.pushManager.getSubscription();
      if (sub) await sub.unsubscribe();
    } catch {
      /* best-effort */
    }
    try {
      await api("/notifications/push/device", { method: "DELETE" });
    } catch {
      /* server may already lack the row */
    }
    set("off");
  }, []);

  return { status, enable, disable, refresh };
}
