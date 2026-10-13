import { Fragment, useId, useRef, type Ref } from "react";
import { Ellipsis, type LucideIcon } from "lucide-react";
import { DropdownMenu as DropdownMenuPrimitive } from "radix-ui";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { cn } from "@/lib/utils";

interface PageMoreMenuEntry {
  key: string;
  label: string;
  /** One line under the label saying what the item does. */
  help: string;
  icon: LucideIcon;
  disabled?: boolean;
  /** Starts a new group: a separator is drawn above it. */
  group?: boolean;
}

export interface PageMoreMenuAction extends PageMoreMenuEntry {
  onSelect: () => void;
  /**
   * False when the action moves focus itself: it then runs once the menu has
   * closed (a menu holds focus while open), and focus does not go back to More.
   */
  returnFocus?: boolean;
  /**
   * True for an action that opens a dialog: it runs once the menu has closed
   * and focus is back on More, so the dialog returns focus there.
   */
  opensDialog?: boolean;
}

/** A setting turned on or off from the menu, drawn with a switch. */
export interface PageMoreMenuSwitch extends PageMoreMenuEntry {
  checked: boolean;
  onCheckedChange: (checked: boolean) => void;
  /**
   * True for a change that asks first in a dialog: the menu closes and the
   * change runs once focus is back on More. Other changes keep the menu open.
   */
  asksFirst?: (checked: boolean) => boolean;
}

export type PageMoreMenuItem = PageMoreMenuAction | PageMoreMenuSwitch;

/** What runs once the menu has closed, and whether focus goes back to More first. */
interface AfterClose {
  run: () => void;
  returnFocus: boolean;
}

function isSwitch(item: PageMoreMenuItem): item is PageMoreMenuSwitch {
  return "checked" in item;
}

const ITEM_CLASS = "items-start gap-3 rounded-[9px] px-2.5 py-2.5";

function ItemText({ id, item }: { id: string; item: PageMoreMenuEntry }) {
  return (
    <>
      <item.icon className="text-muted-foreground mt-0.5" />
      <span className="grid min-w-0 flex-1 gap-0.5">
        <span id={`${id}-label`} className="font-medium">
          {item.label}
        </span>
        <span id={`${id}-help`} className="text-muted-foreground text-[12.5px] leading-snug">
          {item.help}
        </span>
      </span>
    </>
  );
}

function ActionItem({
  item,
  onChosen,
}: {
  item: PageMoreMenuAction;
  onChosen: (after: AfterClose | null) => void;
}) {
  const id = useId();
  return (
    <DropdownMenuItem
      disabled={item.disabled}
      aria-labelledby={`${id}-label`}
      aria-describedby={`${id}-help`}
      onSelect={() => {
        if (item.returnFocus === false || item.opensDialog) {
          onChosen({ run: item.onSelect, returnFocus: item.returnFocus !== false });
          return;
        }
        onChosen(null);
        item.onSelect();
      }}
      className={ITEM_CLASS}
    >
      <ItemText id={id} item={item} />
    </DropdownMenuItem>
  );
}

/** Drawn like the app's Switch; the menu item itself carries the checked state. */
function SwitchLook({ checked }: { checked: boolean }) {
  const state = checked ? "checked" : "unchecked";
  return (
    <span
      aria-hidden
      data-state={state}
      className="data-[state=checked]:bg-primary data-[state=unchecked]:bg-border data-[state=unchecked]:border-muted-foreground/30 mt-0.5 inline-flex h-[1.15rem] w-8 shrink-0 items-center rounded-full border shadow-xs transition-all data-[state=checked]:border-transparent"
    >
      <span
        data-state={state}
        className="bg-foreground data-[state=checked]:bg-primary-foreground block size-4 rounded-full transition-transform data-[state=checked]:translate-x-[calc(100%-2px)]"
      />
    </span>
  );
}

function SwitchItem({
  item,
  onChosen,
}: {
  item: PageMoreMenuSwitch;
  onChosen: (after: AfterClose | null) => void;
}) {
  const id = useId();
  return (
    // The primitive, not the ui wrapper: the switch replaces its check mark.
    <DropdownMenuPrimitive.CheckboxItem
      checked={item.checked}
      disabled={item.disabled}
      aria-labelledby={`${id}-label`}
      aria-describedby={`${id}-help`}
      onSelect={(event) => {
        const next = !item.checked;
        if (item.asksFirst?.(next)) {
          onChosen({ run: () => item.onCheckedChange(next), returnFocus: true });
          return;
        }
        event.preventDefault();
        item.onCheckedChange(next);
      }}
      className={cn(
        "focus:bg-accent focus:text-accent-foreground relative flex cursor-pointer text-sm outline-hidden select-none data-[disabled]:pointer-events-none data-[disabled]:opacity-50 [&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-4",
        ITEM_CLASS,
      )}
    >
      <ItemText id={id} item={item} />
      <SwitchLook checked={item.checked} />
    </DropdownMenuPrimitive.CheckboxItem>
  );
}

/**
 * The page's More menu: the actions a page needs now and then, each with a
 * line of help. `compact` is the square 48px trigger of the phone dock.
 */
export function PageMoreMenu({
  items,
  compact = false,
  triggerRef,
  onOpenChange,
}: {
  items: PageMoreMenuItem[];
  compact?: boolean;
  triggerRef?: Ref<HTMLButtonElement>;
  onOpenChange?: (open: boolean) => void;
}) {
  const afterClose = useRef<AfterClose | null>(null);
  const onChosen = (after: AfterClose | null) => {
    afterClose.current = after;
  };
  return (
    <DropdownMenu onOpenChange={onOpenChange}>
      <DropdownMenuTrigger asChild>
        <Button
          ref={triggerRef}
          type="button"
          variant="outline"
          size={compact ? "icon" : "sm"}
          aria-label={compact ? "More" : undefined}
          className={cn(
            "data-[state=open]:bg-accent",
            compact && "bg-surface/90 size-12 rounded-[14px]",
          )}
        >
          <Ellipsis className={compact ? "size-5" : undefined} />
          {compact ? null : "More"}
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent
        // The dock's More sits at the left edge, the header's at the right.
        align={compact ? "start" : "end"}
        side={compact ? "top" : "bottom"}
        className="w-[min(356px,calc(100vw-2rem))] rounded-[14px] p-1.5"
        onCloseAutoFocus={(event) => {
          const after = afterClose.current;
          afterClose.current = null;
          if (!after) return;
          if (!after.returnFocus) event.preventDefault();
          after.run();
        }}
      >
        {items.map((item, index) => (
          <Fragment key={item.key}>
            {item.group && index > 0 ? <DropdownMenuSeparator /> : null}
            {isSwitch(item) ? (
              <SwitchItem item={item} onChosen={onChosen} />
            ) : (
              <ActionItem item={item} onChosen={onChosen} />
            )}
          </Fragment>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
