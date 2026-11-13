import { useEffect, useState } from "react";
import { formatTime as formatClockTime } from "@/lib/datetime";
import { useT } from "@/i18n";

/**
 * "Ends 7:02 PM": the wall-clock time playback finishes, or null while the
 * length is unknown or nothing is left. It keeps its own tick because the
 * position stands still while paused, and a paused finish time still moves.
 * `rate` is real time per media second: the playback speed, 1 in a Watch Party.
 * Based on Rhainland's end-time label (Silo-Server/silo-server#3916).
 */
export function usePlayerEndTime(
  position: number,
  duration: number,
  rate: number,
  /** The tick runs only while the label can be seen. */
  visible: boolean,
): string | null {
  const t = useT();
  const [now, setNow] = useState(() => Date.now());

  useEffect(() => {
    if (!visible) return;
    const refresh = () => setNow(Date.now());
    const firstTick = window.setTimeout(refresh, 0);
    const tick = window.setInterval(refresh, 1000);
    return () => {
      window.clearTimeout(firstTick);
      window.clearInterval(tick);
    };
    // A new position restarts the tick, so the clock is as fresh as the position.
  }, [visible, position]);

  const remaining = duration - position;
  if (!Number.isFinite(remaining) || duration <= 0 || remaining <= 0) return null;
  const speed = rate > 0 ? rate : 1;
  return t("player.scrubber.endsAt", {
    time: formatClockTime(now + (remaining / speed) * 1000),
  });
}
