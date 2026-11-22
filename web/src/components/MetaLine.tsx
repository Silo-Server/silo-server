import { createContext, Fragment, useContext, type ReactNode } from "react";

import { cn } from "@/lib/utils";

const SEPARATOR = " · ";

/** Up to this many characters a part is kept whole, as the impersonation banner keeps a name. */
const SHORT_NAME = 3;

/**
 * The least a part keeps once the line is cut: a letter and "…", or all of a
 * short text, since a floor wider than the text would leave a gap after it.
 */
function floor(text: string) {
  return [...text].length <= SHORT_NAME ? "min-w-max" : "min-w-[2em]";
}

/** Whether the name's phrase ends the line, so the name is the first part cut. */
const NameEndsLine = createContext(true);

/**
 * A person's name inside a `MetaLine` phrase, passed to `t()` as the value. It
 * reads in its own direction and keeps at least a letter and "…" before the
 * parts around it give up their room. At the end of the line it takes the room
 * the other parts leave, so it is cut first; before other parts it keeps its
 * width, and they are cut first. A short name is kept whole instead: a floor
 * wider than the name would leave a gap after it. `align-with-page` keeps a
 * name narrower than its box against the phrase's words.
 */
export function LineName({ name }: { name: string }) {
  const endsLine = useContext(NameEndsLine);
  return (
    <span
      dir="auto"
      className={cn("align-with-page truncate", floor(name), endsLine && "max-w-max grow basis-0")}
    >
      {name}
    </span>
  );
}

/**
 * One line of details joined with " · " and cut short at its end, where a part
 * can be a phrase from `t()` that names someone with `LineName`. The phrase's
 * words keep the page's direction and stay whole. The line is cut from its end,
 * each part with its own "…", except that a name keeps a letter and "…" before
 * the parts ahead of it are cut.
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
  let afterName = false;
  // Screen readers put a space around each flex item, so they read an inline copy
  // instead: words joined to a name ("김님 작성") stay joined.
  return (
    <>
      <span className="sr-only">
        {groups.map((group, index) => (
          <Fragment key={index}>
            {index > 0 ? SEPARATOR : null}
            {group}
          </Fragment>
        ))}
      </span>
      <span aria-hidden className={cn("flex overflow-hidden whitespace-pre", className)}>
        {groups.map((group, index) => {
          const isText = typeof group === "string";
          // Text after a name takes only the room the name leaves, so it is cut
          // first, down to a letter and "…" like the name.
          const yieldsFirst = isText && afterName;
          afterName ||= !isText;
          return (
            <Fragment key={index}>
              {index > 0 ? <span className="shrink-0">{SEPARATOR}</span> : null}
              {isText ? (
                <span
                  className={cn(
                    "truncate",
                    yieldsFirst && ["max-w-max grow basis-0", floor(group)],
                  )}
                >
                  {group}
                </span>
              ) : (
                <NameEndsLine.Provider value={index === groups.length - 1}>
                  {group}
                </NameEndsLine.Provider>
              )}
            </Fragment>
          );
        })}
      </span>
    </>
  );
}
