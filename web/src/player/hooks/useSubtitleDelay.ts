import { useCallback, useEffect, useRef, useState } from "react";
import { clampSubtitleSyncMs, useSubtitleSyncSetting } from "@/hooks/queries/subtitleSync";

/** How long the Delay control waits after the last nudge before saving. */
const SAVE_AFTER_MS = 600;

/**
 * The player's subtitle delay in ms (positive shows later), bound to the
 * profile's timing offset so a change here follows the profile to every device.
 *
 * A nudge shows at once from a local draft and is saved after a short pause,
 * so holding the button sends one write instead of one per step. Leaving the
 * player saves a nudge still waiting out the pause.
 */
export function useSubtitleDelay(): [number, (ms: number) => void] {
  const { offsetMs: savedMs, save, isLoaded } = useSubtitleSyncSetting();
  const [draftMs, setDraftMs] = useState<number | null>(null);
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const flushRef = useRef<(() => void) | null>(null);
  // Saves run one at a time, so an earlier offset can never land after a later one.
  const queueRef = useRef<Promise<void>>(Promise.resolve());
  // Saves scheduled or queued but not yet settled.
  const [unsettled, setUnsettled] = useState(0);

  // The draft steps aside once every save has settled and the saved value
  // shows it, not when a save resolves: if the refetch after a save fails, or
  // an earlier save lands before a queued one, the value shown would not be
  // the last nudge and the next nudge would build on it. Best effort: a failed
  // save keeps the draft for this session and the next nudge retries, rather
  // than interrupting playback.
  if (draftMs !== null && unsettled === 0 && draftMs === savedMs) setDraftMs(null);

  const setDelayMs = useCallback(
    (ms: number) => {
      // Until the saved offset arrives the control shows the default, and a
      // nudge from it would overwrite the profile's real offset.
      if (!isLoaded) return;
      const next = clampSubtitleSyncMs(ms);
      setDraftMs(next);
      if (timerRef.current) clearTimeout(timerRef.current);
      else setUnsettled((count) => count + 1);
      const flush = () => {
        timerRef.current = null;
        flushRef.current = null;
        queueRef.current = queueRef.current
          .then(() => save(next))
          .catch(() => undefined)
          .finally(() => setUnsettled((count) => count - 1));
      };
      flushRef.current = flush;
      timerRef.current = setTimeout(flush, SAVE_AFTER_MS);
    },
    [isLoaded, save],
  );

  useEffect(
    () => () => {
      if (timerRef.current) clearTimeout(timerRef.current);
      flushRef.current?.();
    },
    [],
  );

  return [draftMs ?? savedMs, setDelayMs];
}
