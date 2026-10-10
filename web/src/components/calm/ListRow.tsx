import type { ButtonHTMLAttributes, CSSProperties, ReactNode, Ref } from "react";
import { EyeOff, GripVertical } from "lucide-react";
import { Switch } from "@/components/ui/switch";
import { cn } from "@/lib/utils";
import { SelectCheckbox } from "./SelectCheckbox";

export interface ListRowSelection {
  selected: boolean;
  label: string;
  onChange: (checked: boolean, extendRange: boolean) => void;
}

export interface ListRowProps {
  /** Written to `data-row-id`, so a page can find the row to scroll it into view. */
  id: string;
  title: string;
  /** Off or hidden: the row shrinks to a dashed line with `collapsedText` after its name. */
  collapsed: boolean;
  collapsedText: ReactNode;
  /** The art at the start of the row, usually a `PosterPeek`. */
  art: ReactNode;
  /** At most one tag after the name. */
  tags?: ReactNode;
  /** The line under the name. */
  meta: ReactNode;
  /** Props for the grip button, from the sortable list. None: the list isn't ordered by hand, so no grip. */
  handleProps?: ButtonHTMLAttributes<HTMLButtonElement> & { ref?: Ref<HTMLButtonElement> };
  /** Select mode: a checkbox takes the grip's place. */
  selection?: ListRowSelection;
  /** The row's switch; its label names what turning it off does. */
  shown: { checked: boolean; label: string; disabled?: boolean; onChange: (on: boolean) => void };
  menu: ReactNode;
  /** A click on the row body opens it; keyboard users open it from the menu. */
  onOpen?: () => void;
  ref?: Ref<HTMLLIElement>;
  style?: CSSProperties;
  dragging?: boolean;
  /** Just added: a short highlight so the eye finds it. */
  highlighted?: boolean;
}

/** The separator between parts of a row's meta line. */
export function MetaDot() {
  return (
    <span aria-hidden className="mx-[7px] inline-block opacity-55">
      ·
    </span>
  );
}

export function Grip({
  title,
  collapsed,
  handleProps,
}: {
  title: string;
  collapsed: boolean;
  handleProps: NonNullable<ListRowProps["handleProps"]>;
}) {
  const { ref, className, ...rest } = handleProps;
  return (
    <button
      ref={ref}
      type="button"
      aria-label={`Move ${title}`}
      className={cn(
        "text-muted-foreground/70 hover:text-foreground focus-visible:ring-ring/50 grid w-7 cursor-grab touch-none place-items-center rounded-lg outline-none focus-visible:ring-[3px] aria-disabled:cursor-not-allowed aria-disabled:opacity-50",
        collapsed ? "h-[30px]" : "h-9",
        className,
      )}
      {...rest}
    >
      <GripVertical className="size-[18px]" />
    </button>
  );
}

/**
 * One line of a calm list: grip (checkbox in select mode; neither on a list
 * that isn't ordered by hand), art, name and meta line, switch, ⋯.
 *
 * A collapsed row is a dashed line, but the element at every position keeps
 * its type and the switch keeps its key, so toggling a row never moves
 * keyboard focus off its switch.
 */
export function ListRow({
  id,
  title,
  collapsed,
  collapsedText,
  art,
  tags,
  meta,
  handleProps,
  selection,
  shown,
  menu,
  onOpen,
  ref,
  style,
  dragging,
  highlighted,
}: ListRowProps) {
  const lead = Boolean(selection || handleProps);
  return (
    <li
      ref={ref}
      style={style}
      data-row-id={id}
      data-highlighted={highlighted || undefined}
      data-selected={selection?.selected || undefined}
      className={cn(
        "group/row hover:bg-accent/60 relative grid items-center gap-2 rounded-[18px] py-[11px] pr-3 pl-2 sm:gap-3.5",
        "before:bg-border/75 before:absolute before:top-0 before:right-4 before:h-px first:before:hidden hover:before:hidden [&:hover+li]:before:hidden",
        // Under 1024px the ⋯ column widens to a 44px touch target.
        lead
          ? "grid-cols-[28px_48px_minmax(0,1fr)_auto_44px] before:left-[124px] sm:grid-cols-[28px_74px_minmax(0,1fr)_auto_44px] lg:grid-cols-[28px_74px_minmax(0,1fr)_auto_36px]"
          : "grid-cols-[48px_minmax(0,1fr)_auto_44px] pl-3 before:left-[96px] sm:grid-cols-[74px_minmax(0,1fr)_auto_44px] lg:grid-cols-[74px_minmax(0,1fr)_auto_36px]",
        collapsed &&
          "border-muted-foreground/30 bg-background/40 my-1.5 border border-dashed py-[5px] before:hidden [&+li]:before:hidden",
        selection?.selected && "bg-accent/75",
        dragging && "bg-surface-raised z-10 shadow-lg",
        highlighted && "bg-accent ring-ring/60 ring-2 transition-[box-shadow,background-color]",
      )}
    >
      {selection ? (
        <span className={cn("grid w-7 place-items-center", collapsed ? "h-[30px]" : "h-9")}>
          <SelectCheckbox
            label={selection.label}
            checked={selection.selected}
            onChange={selection.onChange}
          />
        </span>
      ) : handleProps ? (
        <Grip title={title} collapsed={collapsed} handleProps={handleProps} />
      ) : null}
      {collapsed ? (
        <div
          aria-hidden
          className="text-muted-foreground grid h-[30px] w-12 place-items-center sm:w-[74px]"
        >
          <EyeOff className="size-[17px]" />
        </div>
      ) : (
        art
      )}
      <div className={cn("min-w-0", onOpen && "cursor-pointer")} onClick={onOpen}>
        {collapsed ? (
          <p className="text-muted-foreground truncate text-[13.5px]">
            <b className="text-foreground/80 font-semibold">{title}</b>
            {collapsedText}
          </p>
        ) : (
          <>
            <div className="flex min-w-0 items-center gap-2 text-[15px] font-semibold tracking-[-0.01em]">
              <span className="truncate">{title}</span>
              {tags}
            </div>
            <p className="text-muted-foreground mt-1 truncate text-[13px] leading-[1.45]">{meta}</p>
          </>
        )}
      </div>
      <div className="flex items-center gap-3">
        <Switch
          key="shown"
          checked={shown.checked}
          disabled={shown.disabled}
          // A 44px touch target under 1024px, without changing the layout.
          className="max-lg:relative max-lg:after:absolute max-lg:after:-inset-[13px] max-lg:after:content-['']"
          aria-label={shown.label}
          onCheckedChange={(checked) => shown.onChange(checked === true)}
        />
      </div>
      {menu}
    </li>
  );
}
