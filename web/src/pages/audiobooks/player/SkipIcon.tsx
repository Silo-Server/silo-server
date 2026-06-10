import { RotateCcw, RotateCw } from "lucide-react";

/**
 * Rotate-arrow glyph with the skip interval inset, shared by the mini bar and
 * Now Listening transports. Pair with `className="group"` on the surrounding
 * CircleButton so the arrow flicks in the skip direction while pressed.
 */
export function SkipIcon({
  direction,
  seconds,
  size = "md",
}: {
  direction: "back" | "forward";
  seconds: number;
  size?: "md" | "lg";
}) {
  const Arrow = direction === "back" ? RotateCcw : RotateCw;
  const box = size === "lg" ? "h-7 w-7" : "h-6 w-6";
  const label = size === "lg" ? "text-[8.5px]" : "text-[8px]";
  const flick =
    direction === "back" ? "group-active:-rotate-[18deg]" : "group-active:rotate-[18deg]";
  return (
    <span className={`relative flex items-center justify-center ${box}`}>
      <Arrow
        className={`${box} transition-transform duration-150 ease-out ${flick}`}
        strokeWidth={1.6}
      />
      <span
        className={`absolute inset-0 flex items-center justify-center pb-[1px] font-semibold tracking-tight tabular-nums ${label}`}
      >
        {seconds}
      </span>
    </span>
  );
}
