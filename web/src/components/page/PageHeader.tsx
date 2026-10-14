import type { ReactNode } from "react";

import { cn } from "@/lib/utils";

export interface PageHeaderProps {
  title: ReactNode;
  /** One sentence on what the page is for, under the title. */
  subtitle?: ReactNode;
  /** Buttons or menus level with the title, at the right. */
  actions?: ReactNode;
  /** Content between the title and the subtitle, such as a status chip. */
  eyebrow?: ReactNode;
  className?: string;
}

/**
 * The heading every admin page and settings page opens with. One size, one
 * position and one gap to the content below, so moving between pages never
 * moves the title.
 */
export function PageHeader({ title, subtitle, actions, eyebrow, className }: PageHeaderProps) {
  return (
    <header
      className={cn(
        "page-header mb-8 flex min-w-0 flex-wrap items-start justify-between gap-x-6 gap-y-3",
        className,
      )}
    >
      <div className="min-w-0 space-y-2">
        {eyebrow}
        <h1 className="text-[1.75rem] leading-[1.15] font-bold tracking-[-0.035em] text-balance sm:text-[2rem]">
          {title}
        </h1>
        {subtitle ? (
          <p className="text-muted-foreground max-w-[72ch] text-sm leading-relaxed">{subtitle}</p>
        ) : null}
      </div>
      {actions ? (
        // Stays at the right when the header wraps, so a menu opens over the content.
        <div className="ml-auto flex flex-wrap items-center gap-2">{actions}</div>
      ) : null}
    </header>
  );
}

/**
 * The frame admin pages and user settings pages sit in: full width up to
 * 1560px, then centered, so on a wide window the content does not hug the
 * sidebar. Both areas use it so their pages line up the same way.
 */
export function PageFrame({ className, children }: { className?: string; children: ReactNode }) {
  return <div className={cn("mx-auto w-full max-w-[1560px] min-w-0", className)}>{children}</div>;
}

/**
 * The column a page's content sits in. It starts at the layout's gutter on
 * every page instead of centering, so a narrow page and a wide one share the
 * same left edge and title position. `settings` caps it at a width where
 * labels and controls stay in reach; `full` lets tables and dashboards use the
 * whole screen.
 */
export function PageColumn({
  width = "full",
  className,
  children,
}: {
  width?: "settings" | "full";
  className?: string;
  children: ReactNode;
}) {
  return (
    <div
      className={cn(
        "@container w-full min-w-0",
        width === "settings" ? "max-w-[70rem]" : "max-w-none",
        className,
      )}
    >
      {children}
    </div>
  );
}
