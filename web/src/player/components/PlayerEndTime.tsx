import { useEffect, useState } from "react";
import { formatTime as formatClockTime } from "@/lib/datetime";

interface PlayerEndTimeProps {
  position: number;
  duration: number;
  speed?: number;
  /** The tick only runs while the label can be seen. */
  visible: boolean;
}

/**
 * The wall-clock finish time. It runs off its own tick because the position
 * stands still while paused, and a paused finish time still has to move.
 */
export function PlayerEndTime({ position, duration, speed = 1, visible }: PlayerEndTimeProps) {
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
    // Position restarts this so the clock stays as fresh as the position.
  }, [visible, position]);

  const remaining = duration - position;
  if (!Number.isFinite(remaining) || duration <= 0 || remaining <= 0) return null;

  const rate = speed > 0 ? speed : 1;
  return (
    <>
      <span className="mx-1 text-white/30">·</span>
      <wbr />
      <span className="whitespace-nowrap">
        Ends {formatClockTime(now + (remaining / rate) * 1000)}
      </span>
    </>
  );
}
