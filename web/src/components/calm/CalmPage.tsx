import type { ReactNode } from "react";

import { PageHeader } from "@/components/page/PageHeader";
import { cn } from "@/lib/utils";

/**
 * The 1000px column of a calm list page, with the shared page header, its
 * subtitle and the page's buttons at the right. The column starts at the
 * layout's gutter like every other page. Blocks under the header sit 24px
 * apart, as on the other admin pages. `padBottom` keeps room at the foot for a
 * bar docked at the bottom.
 */
export function CalmPage({
  title,
  subtitle,
  actions,
  padBottom = false,
  children,
}: {
  title: string;
  subtitle: string;
  actions?: ReactNode;
  padBottom?: boolean;
  children: ReactNode;
}) {
  return (
    // One column that may shrink below its content's width, so a row of pills
    // scrolls instead of pushing the list past the right edge of a phone.
    <div className={cn("grid max-w-[1000px] grid-cols-[minmax(0,1fr)]", padBottom && "pb-24")}>
      <PageHeader title={title} subtitle={subtitle} actions={actions} />
      <div className="grid grid-cols-[minmax(0,1fr)] gap-6">{children}</div>
    </div>
  );
}
