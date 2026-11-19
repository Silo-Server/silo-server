import type { ReactNode } from "react";
import { X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { useT } from "@/i18n";

/**
 * The bar that floats at the bottom of a notifications view while rows are
 * selected: what is selected, the actions on it, and a way out. It sticks
 * inside the view's column, so it centers on the content rather than on a
 * window that includes the sidebar.
 */
export function SelectionBar({
  label,
  summary,
  children,
  onClear,
}: {
  /** Names the toolbar for assistive technology. */
  label: string;
  /** What is selected, such as "3 selected". */
  summary: ReactNode;
  /** The actions, separated from the summary by a rule. */
  children: ReactNode;
  onClear: () => void;
}) {
  const t = useT();
  return (
    <div className="pointer-events-none sticky bottom-4 z-40 mt-4 flex justify-center sm:bottom-7">
      <div
        role="toolbar"
        aria-label={label}
        className="bg-popover border-border pointer-events-auto flex flex-wrap items-center gap-1.5 rounded-2xl border py-2 ps-4 pe-2 shadow-2xl"
      >
        <div className="flex items-center gap-2 text-sm">{summary}</div>
        <span className="bg-border mx-1.5 hidden h-5 w-px sm:block" aria-hidden />
        <div className="flex flex-wrap items-center gap-1">{children}</div>
        <span className="bg-border mx-1.5 hidden h-5 w-px sm:block" aria-hidden />
        <Button
          variant="ghost"
          size="icon"
          className="text-muted-foreground h-8 w-8"
          aria-label={t("web.notifications.selection.clear")}
          title={t("web.notifications.selection.clear")}
          onClick={onClear}
        >
          <X className="h-4 w-4" />
        </Button>
      </div>
    </div>
  );
}
