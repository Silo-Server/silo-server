import { RotateCcw, RotateCw } from "lucide-react";

/* ─────────────────────────────────────────────────────────────────────
   Internal building blocks
   ───────────────────────────────────────────────────────────────────── */

/** Invisible placeholder that reserves the exact footprint of a CircleButton
 *  so the playback cluster stays symmetric when a neighboring episode isn't
 *  available (first/last episode in a series). */
export function ClusterSlotSpacer({ size }: { size: "sm" | "md" }) {
  const sizing = size === "md" ? "h-12 w-12 sm:h-14 sm:w-14" : "h-10 w-10 sm:h-11 sm:w-11";
  return <div aria-hidden="true" className={sizing} />;
}

/** Curved arrow with the skip-seconds number centered in the loop. */
export function SkipIcon({
  direction,
  seconds,
}: {
  direction: "back" | "forward";
  seconds: number;
}) {
  const Arrow = direction === "back" ? RotateCcw : RotateCw;
  return (
    <span className="relative flex h-7 w-7 items-center justify-center">
      <Arrow className="h-7 w-7" strokeWidth={1.6} />
      <span className="absolute inset-0 flex items-center justify-center pb-[1px] text-[8.5px] font-semibold tracking-tight tabular-nums">
        {seconds}
      </span>
    </span>
  );
}
