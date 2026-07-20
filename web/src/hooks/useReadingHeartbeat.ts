import { useCallback, useEffect, useRef } from "react";

import { sendReadingHeartbeat } from "@/reader/ebookReaderApi";

const HEARTBEAT_INTERVAL_MS = 30_000;
const ACTIVITY_WINDOW_MS = 60_000;

export function shouldHeartbeat(input: {
  visible: boolean;
  lastActivityAt: number;
  now: number;
}): boolean {
  return input.visible && input.now - input.lastActivityAt <= ACTIVITY_WINDOW_MS;
}

function clamp01(value: number): number {
  if (!Number.isFinite(value)) return 0;
  if (value < 0) return 0;
  if (value > 1) return 1;
  return value;
}

function defaultSend(contentId: string, fraction: number): void {
  sendReadingHeartbeat(contentId, fraction).catch(() => {});
}

export function useReadingHeartbeat(options: {
  contentId: string | null;
  getFraction: () => number;
  send?: (contentId: string, fraction: number) => void;
}): { noteActivity: () => void } {
  const { contentId, getFraction, send } = options;

  // Mirror the latest callbacks into refs so the interval and event
  // listeners below never need to rebind when the caller passes fresh
  // closures on every render.
  const contentIdRef = useRef(contentId);
  useEffect(() => {
    contentIdRef.current = contentId;
  }, [contentId]);
  const getFractionRef = useRef(getFraction);
  useEffect(() => {
    getFractionRef.current = getFraction;
  }, [getFraction]);
  const sendRef = useRef(send);
  useEffect(() => {
    sendRef.current = send;
  }, [send]);

  const lastActivityAtRef = useRef<number | null>(null);
  const intervalRef = useRef<ReturnType<typeof setInterval> | null>(null);

  const stopInterval = useCallback(() => {
    if (intervalRef.current != null) {
      clearInterval(intervalRef.current);
      intervalRef.current = null;
    }
  }, []);

  // Pure re-derivation of shouldHeartbeat from the current activity ref and
  // document visibility; used both by the interval tick and to decide
  // whether to (re)start ticking at all.
  const isFresh = useCallback(() => {
    const lastActivityAt = lastActivityAtRef.current;
    if (lastActivityAt == null) return false;
    const visible = typeof document === "undefined" || !document.hidden;
    return shouldHeartbeat({ visible, lastActivityAt, now: Date.now() });
  }, []);

  const fire = useCallback(() => {
    const id = contentIdRef.current;
    if (!id) return;
    const fraction = clamp01(getFractionRef.current());
    const sendFn = sendRef.current ?? defaultSend;
    sendFn(id, fraction);
  }, []);

  // Starts the interval only if idle/hidden isn't already blocking it, and
  // sends one beat immediately rather than waiting up to 30s for the first
  // tick — covers both the very first activity and resuming from idle.
  const beginActiveWindow = useCallback(() => {
    if (intervalRef.current != null) return;
    if (!isFresh()) return;
    fire();
    intervalRef.current = setInterval(() => {
      if (!isFresh()) {
        stopInterval();
        return;
      }
      fire();
    }, HEARTBEAT_INTERVAL_MS);
  }, [fire, isFresh, stopInterval]);

  const noteActivity = useCallback(() => {
    if (!contentIdRef.current) return;
    lastActivityAtRef.current = Date.now();
    beginActiveWindow();
  }, [beginActiveWindow]);

  useEffect(() => {
    const onVisibilityChange = () => {
      if (document.hidden) {
        stopInterval();
        return;
      }
      beginActiveWindow();
    };
    document.addEventListener("visibilitychange", onVisibilityChange);
    return () => document.removeEventListener("visibilitychange", onVisibilityChange);
  }, [beginActiveWindow, stopInterval]);

  // A new (or null) contentId means a different book context — require a
  // fresh activity signal before beating again rather than carrying stale
  // activity across content.
  useEffect(() => {
    lastActivityAtRef.current = null;
    stopInterval();
    return () => stopInterval();
  }, [contentId, stopInterval]);

  return { noteActivity };
}
