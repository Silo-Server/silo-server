import type { ReactNode } from "react";

export interface SettingsSubheadingProps {
  /** The heading itself, e.g. "Per user". Sentence case; it is uppercased in CSS. */
  children: ReactNode;
  /**
   * One line under the heading, for a cluster whose scope is not obvious from
   * the rows themselves ("Counted per login account…").
   */
  caption?: ReactNode;
}

/**
 * A divider inside a settings group: the one treatment for a sub-grouping label,
 * so "Per user" in Downloads, "Mail Server" in Notifications and "Permission
 * checks" in Infrastructure read as the same kind of thing.
 *
 * Deliberately quieter than a `SettingsGroup` title and than a row label — it
 * separates rows without competing with them, which is what four different
 * hand-rolled versions of this were each guessing at.
 */
export function SettingsSubheading({ children, caption }: SettingsSubheadingProps) {
  return (
    <div className="pt-4 pb-1">
      <h4 className="text-muted-foreground/80 text-[11px] font-semibold tracking-wider uppercase">
        {children}
      </h4>
      {caption ? (
        <p className="text-muted-foreground mt-1 text-xs leading-relaxed">{caption}</p>
      ) : null}
    </div>
  );
}
