import type { ReactNode } from "react";

/**
 * The bar docked at the bottom of narrow screens (under 1024px): More and a
 * full-width Add row, in reach of a thumb.
 */
export function MobileDockBar({ more, addRow }: { more: ReactNode; addRow: ReactNode }) {
  return (
    <div
      role="region"
      aria-label="Page actions"
      className="from-background/0 to-background fixed inset-x-0 bottom-0 z-30 grid grid-cols-[48px_1fr] gap-2.5 bg-gradient-to-b to-30% px-4 pt-[22px] pb-[max(1.25rem,env(safe-area-inset-bottom))]"
    >
      {more}
      {addRow}
    </div>
  );
}
