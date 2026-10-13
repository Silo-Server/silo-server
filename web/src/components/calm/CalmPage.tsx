import type { ReactNode } from "react";
import { cn } from "@/lib/utils";

/**
 * The 1000px column of a calm list page, with its heading, subtitle and the
 * page's buttons at the right. `heading="page"` is a page's own h1;
 * `heading="section"` an h2 under a parent page's heading (Settings).
 * `padBottom` keeps room at the foot for a bar docked at the bottom.
 */
export function CalmPage({
  heading,
  title,
  subtitle,
  actions,
  padBottom = false,
  children,
}: {
  heading: "page" | "section";
  title: string;
  subtitle: string;
  actions?: ReactNode;
  padBottom?: boolean;
  children: ReactNode;
}) {
  return (
    // One column that may shrink below its content's width, so a row of pills
    // scrolls instead of pushing the list past the right edge of a phone.
    <div
      className={cn(
        "mx-auto grid max-w-[1000px] grid-cols-[minmax(0,1fr)] gap-7",
        padBottom && "pb-24",
      )}
    >
      <header className="page-header gap-5">
        {heading === "page" ? (
          <div className="space-y-3">
            <h1 className="page-title text-[clamp(2rem,4vw,3rem)]">{title}</h1>
            <p className="page-subtitle max-w-[76ch] text-sm sm:text-base">{subtitle}</p>
          </div>
        ) : (
          <div className="space-y-3">
            <h2 className="text-2xl font-semibold tracking-tight sm:text-3xl">{title}</h2>
            <p className="text-muted-foreground max-w-2xl text-sm leading-relaxed">{subtitle}</p>
          </div>
        )}
        {actions ? (
          // Stays at the right when the header wraps, so a menu opens over the list.
          <div className="ml-auto flex flex-wrap items-center gap-2">{actions}</div>
        ) : null}
      </header>
      {children}
    </div>
  );
}
