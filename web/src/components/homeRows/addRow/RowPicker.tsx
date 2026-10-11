import { useId, useRef, useState } from "react";
import { Button } from "@/components/ui/button";
import type { PickerCard, PickerGroup, RowGroup } from "@/lib/homeRows/catalog";
import { cn } from "@/lib/utils";
import { PickerRail } from "./PickerRail";
import { groupPanelId, groupTabId } from "./pickerIds";
import { RowCard } from "./RowCard";

/** Ignore scroll-spy updates this long after a tab click starts a smooth scroll. */
const TAB_SCROLL_LOCK_MS = 700;

/**
 * Step 1: every kind of row, grouped, with a rail of groups that scrolls the
 * list. Picking a card (or one of its variant chips) moves on to step 2.
 */
export function RowPicker({
  groups,
  query,
  onClearSearch,
  pageLabel,
  typesOnPage,
  narrow,
  onPick,
}: {
  groups: PickerGroup[];
  query: string;
  onClearSearch: () => void;
  pageLabel: string;
  typesOnPage: ReadonlySet<string>;
  /** Below 1024px: chip rail on top, one column, variants as text. */
  narrow: boolean;
  onPick: (card: PickerCard, presetKey?: string) => void;
}) {
  const baseId = useId();
  const pane = useRef<HTMLDivElement>(null);
  const [selected, setSelected] = useState<RowGroup | undefined>();
  const lockedUntil = useRef(0);
  const active = groups.some((group) => group.group === selected) ? selected : groups[0]?.group;

  function selectGroup(group: RowGroup) {
    setSelected(group);
    lockedUntil.current = Date.now() + TAB_SCROLL_LOCK_MS;
    document
      .getElementById(groupPanelId(baseId, group))
      ?.scrollIntoView?.({ block: "start", behavior: "smooth" });
  }

  function followScroll() {
    const list = pane.current;
    if (!list || Date.now() < lockedUntil.current) return;
    const top = list.getBoundingClientRect().top;
    let current = groups[0]?.group;
    for (const group of groups) {
      const section = document.getElementById(groupPanelId(baseId, group.group));
      if (section && section.getBoundingClientRect().top - top <= 24) current = group.group;
    }
    if (current !== active) setSelected(current);
  }

  if (groups.length === 0) {
    return (
      <div className="grid place-items-center gap-3 px-6 py-16 text-center">
        <p className="text-muted-foreground text-sm" role="status">
          No rows match “{query}”.
        </p>
        <Button type="button" variant="outline" size="sm" onClick={onClearSearch}>
          Clear search
        </Button>
      </div>
    );
  }

  return (
    <div
      className={cn(
        "grid min-h-0 flex-1",
        narrow
          ? "grid-cols-[minmax(0,1fr)] grid-rows-[auto_minmax(0,1fr)] gap-3"
          : "grid-cols-[232px_minmax(0,1fr)]",
      )}
    >
      <div className={cn(!narrow && "border-border overflow-y-auto border-r px-3 py-4")}>
        <PickerRail
          baseId={baseId}
          groups={groups}
          active={active}
          onSelect={selectGroup}
          orientation={narrow ? "horizontal" : "vertical"}
        />
        {narrow ? null : (
          <p className="border-border text-muted-foreground mx-1 mt-3.5 border-t px-2 pt-3.5 text-[12.5px] leading-[1.55]">
            Rows marked <b className="text-foreground font-medium">On {pageLabel}</b> are already
            here. You can add them again with other settings.
          </p>
        )}
      </div>
      <div
        ref={pane}
        onScroll={followScroll}
        className={cn(
          "grid min-h-0 content-start gap-6 overflow-y-auto pb-6",
          narrow ? "px-5" : "px-6 pt-[18px]",
        )}
      >
        {groups.map((group) => (
          <section
            key={group.group}
            id={groupPanelId(baseId, group.group)}
            role="tabpanel"
            aria-labelledby={groupTabId(baseId, group.group)}
            className="scroll-mt-4"
          >
            <h3 className="mb-3 flex items-baseline gap-2.5 text-[13px] font-semibold">
              {group.label}
              <span className="text-muted-foreground font-normal">{group.blurb}</span>
            </h3>
            <div className={cn("grid gap-2.5", !narrow && "grid-cols-2")}>
              {group.cards.map((card) => (
                <RowCard
                  key={card.type}
                  card={card}
                  onPage={typesOnPage.has(card.type)}
                  pageLabel={pageLabel}
                  compact={narrow}
                  onPick={(presetKey) => onPick(card, presetKey)}
                />
              ))}
            </div>
          </section>
        ))}
      </div>
    </div>
  );
}
