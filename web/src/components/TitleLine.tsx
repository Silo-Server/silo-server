import { Children, type ReactNode } from "react";
import { cn } from "@/lib/utils";

/**
 * A metadata title inside a line of translated words ("S01E02 · Title",
 * "Play Title"), cut in its own box and in its own direction so an English
 * title after Arabic words keeps its start. Pass it to `t()` as the title and
 * render the result with `TitleLineParts`. It takes what the words leave, up
 * to its own width, and in a wrapping line moves to a second line when not
 * even its first letter and "…" fit. `basis-auto` gives a title the line
 * first: what follows it moves to the next line before the title is cut.
 */
export function LineTitle({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <span dir="auto" className={cn("bidi-text max-w-max grow basis-[2.5em] truncate", className)}>
      {children}
    </span>
  );
}

/**
 * The parts of a line that holds a `LineTitle`. The line itself takes
 * `flex flex-wrap overflow-hidden` (no `flex-wrap` where it can't grow taller,
 * as in a pill or a button). The words keep their width and are cut only when
 * they alone fill the line; `pre` keeps the spaces at their edges.
 */
export function TitleLineParts({ children }: { children: ReactNode }) {
  return Children.map(children, (part) =>
    typeof part === "string" ? (
      <span className="max-w-full shrink-0 overflow-hidden text-ellipsis whitespace-pre">
        {part}
      </span>
    ) : (
      part
    ),
  );
}
