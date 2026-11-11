import {
  useEffect,
  useLayoutEffect,
  useRef,
  type KeyboardEvent,
  type ReactNode,
  type RefObject,
} from "react";
import { X } from "lucide-react";
import "@/styles/player-hud.css";
import { useCoarsePointer } from "../hooks/useCoarsePointer";
import { useT } from "@/i18n";
import { readingArrowKey } from "@/lib/direction";

interface PlayerMenuSurfaceProps {
  children: ReactNode;
  /** Placement and sizing for the desktop popover; the touch sheet ignores it. */
  className?: string;
  onClose: () => void;
  /** Names the menu; shown as its header and read as its accessible name. */
  title?: string;
  /**
   * `menu` for a list of choices. `dialog` for a panel that also holds other
   * controls (subtitle delay), with its choices in nested menus.
   */
  role?: "menu" | "dialog";
  onKeyDown?: (event: KeyboardEvent<HTMLDivElement>) => void;
  /**
   * The menu's trigger wrapper. A pointer press outside it closes the menu.
   * Focus-based dismissal alone is not enough: Safari does not focus buttons
   * on click, so the trigger never blurs.
   */
  anchorRef?: RefObject<HTMLElement | null>;
}

const ITEM_SELECTOR = '[role^="menuitem"]:not([disabled]):not([aria-disabled="true"])';

/** Arrow keys, Home and End move between a menu's items, in document order. */
function moveFocus(surface: HTMLElement, event: KeyboardEvent<HTMLDivElement>) {
  const items = [...surface.querySelectorAll<HTMLElement>(ITEM_SELECTOR)];
  if (items.length === 0) return;
  const current = items.indexOf(document.activeElement as HTMLElement);
  // Off the list (a dialog's Delay buttons, say), the arrows are not the
  // list's to move: jumping to its first or last item skipped the controls
  // in between.
  if (current === -1 && event.key.startsWith("Arrow")) return;
  let next: number;
  // Left and right read along the menu's direction: right to left, ArrowLeft
  // is the next item.
  switch (readingArrowKey(event)) {
    case "ArrowDown":
    case "ArrowRight":
      next = current < items.length - 1 ? current + 1 : 0;
      break;
    case "ArrowUp":
    case "ArrowLeft":
      next = current > 0 ? current - 1 : items.length - 1;
      break;
    case "Home":
      next = 0;
      break;
    case "End":
      next = items.length - 1;
      break;
    default:
      return;
  }
  event.preventDefault();
  items[next]?.focus();
}

export function PlayerMenuSurface({
  children,
  className = "",
  onClose,
  title,
  role = "menu",
  onKeyDown,
  anchorRef,
}: PlayerMenuSurfaceProps) {
  const t = useT();
  const isCoarsePointer = useCoarsePointer();
  const surfaceRef = useRef<HTMLDivElement>(null);
  // Whether the viewer is driving the menu from the keyboard: a key pressed in
  // it says yes, a pointer press in it says no.
  const keyboardRef = useRef(false);

  // Opening moves focus into the menu, onto the checked item when there is one,
  // so arrow keys work at once. Closing from the keyboard hands focus back to
  // whatever opened the menu; a pointer close leaves focus where the pointer put it.
  useLayoutEffect(() => {
    const surface = surfaceRef.current;
    if (!surface) return;
    const opener = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const item =
      surface.querySelector<HTMLElement>(
        '[role^="menuitem"][aria-checked="true"], [role^="menuitem"][aria-current="true"]',
      ) ?? surface.querySelector<HTMLElement>(ITEM_SELECTOR);
    item?.focus({ preventScroll: true });
    item?.scrollIntoView?.({ block: "nearest" });
    const pointerPress = () => {
      keyboardRef.current = false;
    };
    surface.addEventListener("pointerdown", pointerPress);
    return () => {
      surface.removeEventListener("pointerdown", pointerPress);
      const active = document.activeElement;
      const focusWasInside = !active || active === document.body || surface.contains(active);
      if (keyboardRef.current && focusWasInside && opener?.isConnected) {
        opener.focus({ preventScroll: true });
      }
    };
  }, []);

  useEffect(() => {
    const handleKeyDown = (event: globalThis.KeyboardEvent) => {
      if (event.key === "Escape") onClose();
    };
    document.addEventListener("keydown", handleKeyDown);
    return () => document.removeEventListener("keydown", handleKeyDown);
  }, [onClose]);

  // Coarse pointers get a full-screen backdrop button instead.
  useEffect(() => {
    if (isCoarsePointer || !anchorRef) return;
    const handlePointerDown = (event: PointerEvent) => {
      if (!anchorRef.current?.contains(event.target as Node)) onClose();
    };
    document.addEventListener("pointerdown", handlePointerDown, true);
    return () => document.removeEventListener("pointerdown", handlePointerDown, true);
  }, [anchorRef, isCoarsePointer, onClose]);

  const handleKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    keyboardRef.current = true;
    onKeyDown?.(event);
    if (!event.defaultPrevented && surfaceRef.current) moveFocus(surfaceRef.current, event);
  };

  if (!isCoarsePointer) {
    return (
      <div
        ref={surfaceRef}
        role={role}
        aria-label={title}
        className={`player-menu-surface hud-panel ${className}`}
        onKeyDown={handleKeyDown}
        onClick={(event) => event.stopPropagation()}
      >
        {title ? (
          <div aria-hidden="true" className="hud-panel-header">
            {title}
          </div>
        ) : null}
        {children}
      </div>
    );
  }

  return (
    <>
      <button
        type="button"
        className="fixed inset-0 z-40 bg-black/55"
        aria-label={t("web.player.menu.close")}
        onClick={(event) => {
          event.stopPropagation();
          onClose();
        }}
      />
      <div
        className="hud-sheet fixed bottom-0 z-50 flex flex-col"
        onClick={(event) => event.stopPropagation()}
      >
        <span aria-hidden="true" className="hud-sheet-handle" />
        <div className="hud-panel-header">
          <span aria-hidden="true">{title}</span>
          <button
            type="button"
            className="hud-icon-btn -me-2"
            aria-label={t("web.ui.close")}
            onClick={onClose}
          >
            <X />
          </button>
        </div>
        <div
          ref={surfaceRef}
          role={role}
          aria-label={title}
          className="hud-panel-scroll min-h-0 flex-1 pb-[max(0.75rem,env(safe-area-inset-bottom))]"
          onKeyDown={handleKeyDown}
        >
          {children}
        </div>
      </div>
    </>
  );
}
