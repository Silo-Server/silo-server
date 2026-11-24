import { createContext, Fragment, useContext, type ReactNode } from "react";

import { nameFloor } from "@/lib/direction";
import { cn } from "@/lib/utils";

const SEPARATOR = " · ";

/**
 * A part that ends a line of several takes only the room the others leave, so it
 * is cut first; the others share what is left once it is down to its floor.
 */
const ENDS_LINE = "max-w-max grow basis-0";

/** Whether the phrase ends the line, so its `LineName` is the first part cut. */
const EndsLine = createContext(true);

/**
 * User text inside a `MetaLine` phrase (a person's name, a note), passed to
 * `t()` as the value. It reads in its own direction, so a cut one keeps its
 * start, and keeps a letter and "…" when the line is cut. `align-with-page`
 * keeps text narrower than its box against the phrase's words. A metadata title
 * in a line of translated words is a `LineTitle` (`TitleLine.tsx`) instead.
 */
export function LineName({ name }: { name: string }) {
  const endsLine = useContext(EndsLine);
  return (
    <span
      dir="auto"
      className={cn("align-with-page truncate", nameFloor(name), endsLine && ENDS_LINE)}
    >
      {name}
    </span>
  );
}

/**
 * One line of details joined with " · " and cut short, where a part can be a
 * phrase from `t()` that holds user text in a `LineName`. The phrase's words keep
 * the page's direction and stay whole. The part at the end of the line is cut
 * first; then the others, each with its own "…" and keeping a letter and "…".
 */
export function MetaLine({ parts, className }: { parts: ReactNode[]; className?: string }) {
  const groups: ReactNode[] = [];
  for (const part of parts) {
    if (part === null || part === undefined || part === false || part === "") continue;
    const last = groups.at(-1);
    if (typeof part === "string" && typeof last === "string") {
      groups[groups.length - 1] = last + SEPARATOR + part;
    } else {
      groups.push(part);
    }
  }
  if (groups.length === 0) return null;
  const lastIndex = groups.length - 1;
  // Screen readers put a space around each flex item, so they read an inline copy
  // instead: words joined to a name ("김님 작성") stay joined. The copy is placed
  // in the line's own box, so in a scrolling list it can't stretch the page, and
  // it is left out of a selection, so a copied line isn't doubled.
  return (
    <span className="relative block">
      <span className="sr-only select-none">
        {groups.map((group, index) => (
          <Fragment key={index}>
            {index > 0 ? SEPARATOR : null}
            {group}
          </Fragment>
        ))}
      </span>
      <span aria-hidden className={cn("flex overflow-hidden whitespace-pre", className)}>
        {groups.map((group, index) => (
          <Fragment key={index}>
            {index > 0 ? <span className="shrink-0">{SEPARATOR}</span> : null}
            {typeof group === "string" ? (
              <span className={cn("truncate", nameFloor(group), index === lastIndex && ENDS_LINE)}>
                {group}
              </span>
            ) : (
              <EndsLine.Provider value={index === lastIndex}>{group}</EndsLine.Provider>
            )}
          </Fragment>
        ))}
      </span>
    </span>
  );
}
