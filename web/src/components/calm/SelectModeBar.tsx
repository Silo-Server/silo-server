import { Fragment, useId, type ReactNode, type Ref } from "react";
import type { LucideIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { SelectCheckbox } from "./SelectCheckbox";

export interface SelectModeAction {
  key: string;
  label: string;
  icon: LucideIcon;
  onClick: () => void;
  destructive?: boolean;
  /** A thin divider is drawn before it. */
  separated?: boolean;
  /** Off for this selection only, e.g. nothing picked that it applies to. */
  disabled?: boolean;
  /** The bar's note says what it passes over, so a screen reader reads it with the button. */
  explainedByNote?: boolean;
}

/**
 * The floating bar of select mode: how many items are picked and what can be
 * done to them. Actions refuse more than `limit` items at a time.
 *
 * The bar is fixed to the viewport over the content column rather than
 * sticky: AdminLayout clips horizontal overflow, which would hold a sticky bar
 * at the end of the list. The page reserves room at its foot so the last item
 * is never under the bar.
 */
export function SelectModeBar({
  count,
  limit,
  noun,
  actions,
  busy = false,
  note,
}: {
  count: number;
  limit: number;
  /** What is selected, plural: "rows", "collections". */
  noun: string;
  actions: SelectModeAction[];
  busy?: boolean;
  /** One line under the bar about what the actions will pass over. */
  note?: ReactNode;
}) {
  const noteId = useId();
  const tooMany = count > limit;
  const disabled = busy || count === 0 || tooMany;
  return (
    <div className="pointer-events-none fixed inset-x-0 bottom-0 z-30 flex flex-col items-center gap-1.5 px-4 pb-[max(1rem,env(safe-area-inset-bottom))] lg:left-[240px]">
      <div
        role="group"
        aria-label={`Selected ${noun}`}
        className="bg-popover/95 border-border pointer-events-auto flex max-w-full flex-wrap items-center justify-center gap-1 rounded-2xl border py-[7px] pr-[7px] pl-4 shadow-[0_24px_60px_-20px_rgb(0_0_0/0.9)] backdrop-blur"
      >
        <span role="status" className="mr-2 text-sm font-semibold whitespace-nowrap">
          {count} selected
          {tooMany ? (
            <span className="text-warning ml-2 font-normal">
              Select up to {limit} {noun} at a time.
            </span>
          ) : null}
        </span>
        {actions.map((action) => (
          <Fragment key={action.key}>
            {action.separated ? (
              <span aria-hidden className="bg-border mx-1 h-[22px] w-px" />
            ) : null}
            <Button
              variant="ghost"
              size="sm"
              disabled={disabled || action.disabled}
              aria-describedby={note && action.explainedByNote ? noteId : undefined}
              onClick={action.onClick}
              className={cn(
                action.destructive &&
                  "text-destructive hover:text-destructive [&_svg]:text-destructive",
              )}
            >
              <action.icon />
              <span className="max-sm:sr-only">{action.label}</span>
            </Button>
          </Fragment>
        ))}
      </div>
      {note ? (
        <p
          id={noteId}
          className="text-muted-foreground bg-background/80 rounded-full px-2.5 text-[12.5px] backdrop-blur"
        >
          {note}
        </p>
      ) : null}
    </div>
  );
}

/** The top line of the list in select mode: Select all, the limit, and Done. */
export function SelectAllHeader({
  count,
  selectedCount,
  limit,
  noun,
  onSelectAll,
  onDone,
  ref,
}: {
  /** How many items the list holds. */
  count: number;
  selectedCount: number;
  limit: number;
  /** What is selected, plural: "rows", "collections". */
  noun: string;
  onSelectAll: (checked: boolean) => void;
  onDone: () => void;
  ref?: Ref<HTMLButtonElement>;
}) {
  let checked: boolean | "indeterminate" = false;
  if (count > 0 && selectedCount === count) checked = true;
  else if (selectedCount > 0) checked = "indeterminate";
  return (
    <div className="border-border/75 mb-0.5 flex items-center gap-3 border-b py-2.5 pr-3.5 pl-2 text-[13.5px]">
      <span className="grid w-7 place-items-center">
        <SelectCheckbox
          ref={ref}
          label="Select all"
          disabled={count === 0}
          checked={checked}
          onChange={onSelectAll}
        />
      </span>
      <span aria-hidden className="font-medium">
        Select all
      </span>
      <span className="text-muted-foreground text-[13px]">
        Up to {limit} {noun} at a time
      </span>
      <Button size="sm" variant="ghost" className="ml-auto" onClick={onDone}>
        Done
      </Button>
    </div>
  );
}
