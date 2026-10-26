import type { KeyboardEvent } from "react";
import type { PickerGroup, RowGroup } from "@/lib/homeRows/catalog";
import { cn } from "@/lib/utils";
import { GROUP_ICONS, GROUP_TINTS } from "../rowIcons";
import { groupTabId, groupPanelId } from "./pickerIds";

/**
 * The picker's groups as tabs that scroll the card list to their group.
 * Vertical beside the cards on wide screens, a row of chips on phones.
 * Arrow keys move between tabs (and scroll), Home and End jump to the ends.
 */
export function PickerRail({
  baseId,
  groups,
  active,
  onSelect,
  orientation,
}: {
  baseId: string;
  groups: PickerGroup[];
  active: RowGroup | undefined;
  onSelect: (group: RowGroup) => void;
  orientation: "vertical" | "horizontal";
}) {
  const vertical = orientation === "vertical";
  const activeIndex = Math.max(
    0,
    groups.findIndex((group) => group.group === active),
  );

  function handleKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    const back = vertical ? "ArrowUp" : "ArrowLeft";
    const forward = vertical ? "ArrowDown" : "ArrowRight";
    let next: number | null = null;
    if (event.key === back) next = (activeIndex - 1 + groups.length) % groups.length;
    else if (event.key === forward) next = (activeIndex + 1) % groups.length;
    else if (event.key === "Home") next = 0;
    else if (event.key === "End") next = groups.length - 1;
    if (next === null) return;
    event.preventDefault();
    const group = groups[next]!.group;
    onSelect(group);
    document.getElementById(groupTabId(baseId, group))?.focus();
  }

  return (
    <div
      role="tablist"
      aria-label="Kinds of rows"
      aria-orientation={orientation}
      onKeyDown={handleKeyDown}
      className={cn(
        vertical
          ? "grid content-start gap-0.5"
          : "flex gap-2 overflow-x-auto px-5 pb-1 [scrollbar-width:none]",
      )}
    >
      {groups.map((group, index) => {
        const Icon = GROUP_ICONS[group.group];
        const selected = index === activeIndex;
        return (
          <button
            key={group.group}
            id={groupTabId(baseId, group.group)}
            type="button"
            role="tab"
            aria-selected={selected}
            aria-controls={groupPanelId(baseId, group.group)}
            tabIndex={selected ? 0 : -1}
            onClick={() => onSelect(group.group)}
            className={cn(
              "focus-visible:ring-ring/50 outline-none focus-visible:ring-[3px]",
              vertical
                ? "text-muted-foreground hover:text-foreground aria-selected:bg-accent aria-selected:text-foreground aria-selected:ring-border flex items-center gap-2.5 rounded-[10px] px-3 py-[9px] text-left text-sm aria-selected:ring-1 aria-selected:ring-inset"
                : "border-border text-foreground/80 aria-selected:bg-foreground aria-selected:text-background h-10 shrink-0 rounded-full border px-4 text-sm font-medium whitespace-nowrap",
            )}
          >
            {vertical ? (
              <span
                aria-hidden
                className={cn(
                  "grid size-[22px] place-items-center rounded-[7px]",
                  GROUP_TINTS[group.group],
                )}
              >
                <Icon className="size-[13px]" />
              </span>
            ) : null}
            {group.label}
            {vertical ? (
              <span className="ml-auto text-xs tabular-nums opacity-80">
                {group.cards.length}
                <span className="sr-only"> kinds</span>
              </span>
            ) : null}
          </button>
        );
      })}
    </div>
  );
}
