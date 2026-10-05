import type { ReactNode } from "react";

import { useMediaQuery } from "@/hooks/useMediaQuery";
import { cn } from "@/lib/utils";

/** Below 1024px: one column with a sticky section switcher. */
export const PHONE_QUERY = "(max-width: 1023px)";
/** From 1280px: Contents beside a sticky Details / Where it shows column. */
export const COLUMNS_QUERY = "(min-width: 1280px)";

type Panel = "contents" | "details" | "where";

/**
 * The editor page's layout, the same for every type and scope.
 *
 * Two columns only from 1280px (`xl`): at 1024px the app sidebar leaves the
 * Contents column too narrow (#1610). From 1024px to 1279px the panels stack
 * in one column (Contents, Details, Where it shows). Below 1024px a sticky
 * switcher jumps between them, and create mode puts Details first.
 */
export function CollectionEditorShell({
  header,
  banner,
  contents,
  contentsLabel,
  details,
  where,
  createMode,
  footer,
}: {
  header: ReactNode;
  banner?: ReactNode;
  contents: ReactNode;
  /** The switcher's name for the Contents panel: "Titles", "Rules" or "The list". */
  contentsLabel: string;
  details: ReactNode;
  where: ReactNode;
  createMode: boolean;
  footer?: ReactNode;
}) {
  const phone = useMediaQuery(PHONE_QUERY);
  const columns = useMediaQuery(COLUMNS_QUERY);
  const layout = phone ? "phone" : columns ? "columns" : "stacked";
  const detailsFirst = phone && createMode;
  const sections: Array<[Panel, string]> = [
    ["contents", contentsLabel],
    ["details", "Details"],
    ["where", "Where it shows"],
  ];
  if (detailsFirst) sections.unshift(sections.splice(1, 1)[0]!);

  return (
    <div className="page-shell py-4 sm:py-6">
      <div
        data-layout={layout}
        className="mx-auto grid max-w-[1000px] grid-cols-[minmax(0,1fr)] gap-6"
      >
        {header}
        {phone ? (
          <nav
            aria-label="Editor sections"
            className="bg-background/90 sticky top-0 z-20 -mx-1 flex gap-1 rounded-xl p-1 backdrop-blur"
          >
            {sections.map(([panel, label]) => (
              <button
                key={panel}
                type="button"
                className="hover:bg-accent h-11 flex-1 rounded-lg px-2 text-[13.5px] font-medium"
                onClick={() =>
                  document
                    .querySelector(`[data-panel="${panel}"]`)
                    ?.scrollIntoView({ behavior: "smooth", block: "start" })
                }
              >
                {label}
              </button>
            ))}
          </nav>
        ) : null}
        {banner}
        <div
          data-testid="editor-grid"
          className="grid grid-cols-[minmax(0,1fr)] items-start gap-6 xl:grid-cols-[minmax(0,1fr)_340px]"
        >
          <div className="min-w-0">{contents}</div>
          {/* On a phone the column dissolves into the page grid, so create mode
              can lift Details above the titles. */}
          <div className="grid min-w-0 gap-6 max-lg:contents xl:sticky xl:top-6">
            <div className={cn("min-w-0", detailsFirst && "order-first")}>{details}</div>
            <div className="min-w-0">{where}</div>
          </div>
        </div>
        {footer}
      </div>
    </div>
  );
}
