import { Fragment, type ReactNode } from "react";

import { cn } from "@/lib/utils";

const SEPARATOR = " · ";

/**
 * A person's name inside a `MetaLine` phrase, passed to `t()` as the value. It
 * reads in its own direction, takes the room the other parts leave, and keeps
 * at least a letter and "…" before they give up theirs. `align-with-page` keeps
 * a name shorter than that minimum against the phrase's words.
 */
export function LineName({ name }: { name: string }) {
  return (
    <span dir="auto" className="align-with-page max-w-max min-w-[2em] grow basis-0 truncate">
      {name}
    </span>
  );
}

/**
 * One line of details joined with " · " and cut short at its end, where a part
 * can be a phrase from `t()` that names someone with `LineName`. The phrase's
 * words keep the page's direction and stay whole; its name is cut first, then
 * the plain parts, each with its own "…".
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
  return (
    <span className={cn("flex overflow-hidden whitespace-pre", className)}>
      {groups.map((group, index) => (
        <Fragment key={index}>
          {index > 0 ? <span className="shrink-0">{SEPARATOR}</span> : null}
          {typeof group === "string" ? <span className="truncate">{group}</span> : group}
        </Fragment>
      ))}
    </span>
  );
}
