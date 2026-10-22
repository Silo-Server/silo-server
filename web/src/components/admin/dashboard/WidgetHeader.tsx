import type { ReactNode } from "react";

import { CardHeader, CardTitle } from "@/components/ui/card";

/**
 * The header of a widget with a range picker: the title, then a group with the
 * widget's summary or link and the picker.
 *
 * The title takes whatever width the group leaves and wraps within it, so at
 * the default sizes the header is one row. When the group and the title's
 * longest word no longer fit side by side, as at a widget's minimum span just
 * above the twelve-column breakpoint, the group moves under the title, and its
 * items stack right-aligned if they still do not fit, rather than running past
 * the card's edge.
 */
export function WidgetHeader({ title, children }: { title: string; children: ReactNode }) {
  return (
    <CardHeader className="flex shrink-0 flex-row flex-wrap items-center justify-between gap-2 space-y-0 pb-3">
      <CardTitle className="flex-1 text-sm font-bold">{title}</CardTitle>
      <div className="ml-auto flex min-w-0 flex-wrap items-center justify-end gap-2">
        {children}
      </div>
    </CardHeader>
  );
}
