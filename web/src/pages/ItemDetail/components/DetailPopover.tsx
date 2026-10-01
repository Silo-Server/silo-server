import {
  cloneElement,
  type ComponentProps,
  type ReactElement,
  type ReactNode,
  useCallback,
  useEffect,
  useLayoutEffect,
  useId,
  useRef,
  useState,
} from "react";
import { createPortal } from "react-dom";

import { cn } from "@/lib/utils";

// Marks a control that dismisses the popover when activated. Opt-in per
// control: a bare `closest("button")` rule would also dismiss on clicks that
// are not a selection, and would silently swallow any future control placed
// inside a popover.
export const DETAIL_POPOVER_CLOSE_ATTR = "data-detail-popover-close";

interface DetailPopoverProps {
  trigger: ReactElement<ComponentProps<"button">>;
  children: ReactNode;
  contentClassName?: string;
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
  positionKey?: string | number | boolean;
}

export default function DetailPopover({
  trigger,
  children,
  contentClassName,
  open: controlledOpen,
  onOpenChange,
  positionKey,
}: DetailPopoverProps) {
  const [uncontrolledOpen, setUncontrolledOpen] = useState(false);
  const [position, setPosition] = useState<{ left: number; top: number } | null>(null);
  const generatedId = useId();
  const triggerId = trigger.props.id ?? `${generatedId}-trigger`;
  const contentId = `${generatedId}-content`;
  const triggerRef = useRef<HTMLSpanElement>(null);
  const contentRef = useRef<HTMLDivElement>(null);
  const scrollFrameRef = useRef<number | null>(null);
  const open = controlledOpen ?? uncontrolledOpen;

  const setOpen = useCallback(
    (nextOpen: boolean) => {
      if (controlledOpen === undefined) {
        setUncontrolledOpen(nextOpen);
        if (!nextOpen) {
          setPosition(null);
        }
      }
      onOpenChange?.(nextOpen);
    },
    [controlledOpen, onOpenChange],
  );

  const positionContent = useCallback(() => {
    const triggerElement = triggerRef.current;
    const content = contentRef.current;
    if (!triggerElement || !content) return;

    const viewportPadding = 8;
    const gap = 8;
    const triggerRect = triggerElement.getBoundingClientRect();
    const contentRect = content.getBoundingClientRect();
    const spaceBelow = window.innerHeight - triggerRect.bottom - viewportPadding;
    const top =
      spaceBelow >= contentRect.height + gap
        ? triggerRect.bottom + gap
        : Math.max(viewportPadding, triggerRect.top - contentRect.height - gap);
    const left = Math.min(
      window.innerWidth - contentRect.width - viewportPadding,
      Math.max(viewportPadding, triggerRect.left),
    );
    setPosition({ left, top });
  }, []);

  useLayoutEffect(() => {
    if (!open) return;

    positionContent();

    window.addEventListener("resize", positionContent);
    return () => window.removeEventListener("resize", positionContent);
  }, [open, positionContent, positionKey]);

  useLayoutEffect(() => {
    if (!open) return;
    const triggerElement = triggerRef.current;
    contentRef.current?.querySelector<HTMLElement>("button:not(:disabled)")?.focus({
      preventScroll: true,
    });

    // However the popover closes — Escape, activating an entry, the owner
    // flipping `open` — focus must come back to the trigger. Unmounting the
    // portal otherwise drops it on <body> and a keyboard user restarts from the
    // top of the page.
    return () => {
      const active = document.activeElement;
      if (active && active !== document.body) return;
      triggerElement?.querySelector<HTMLElement>("button")?.focus({ preventScroll: true });
    };
  }, [open]);

  useEffect(() => {
    if (!open) return;

    const close = () => setOpen(false);
    const handlePointerDown = (event: PointerEvent) => {
      const target = event.target as Node;
      if (!contentRef.current?.contains(target) && !triggerRef.current?.contains(target)) {
        close();
      }
    };
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key !== "Escape") return;
      event.preventDefault();
      close();
    };
    const handleFocusIn = (event: FocusEvent) => {
      const target = event.target as Node;
      if (!contentRef.current?.contains(target) && !triggerRef.current?.contains(target)) {
        close();
      }
    };
    // Stay anchored to the trigger while the page scrolls rather than
    // dismissing on every wheel nudge. One measurement per frame at most.
    const handleScroll = (event: Event) => {
      const target = event.target;
      if (target instanceof Node && contentRef.current?.contains(target)) return;
      if (scrollFrameRef.current !== null) return;
      scrollFrameRef.current = requestAnimationFrame(() => {
        scrollFrameRef.current = null;
        positionContent();
      });
    };

    document.addEventListener("pointerdown", handlePointerDown, true);
    document.addEventListener("keydown", handleKeyDown);
    document.addEventListener("focusin", handleFocusIn);
    window.addEventListener("scroll", handleScroll, true);
    return () => {
      document.removeEventListener("pointerdown", handlePointerDown, true);
      document.removeEventListener("keydown", handleKeyDown);
      document.removeEventListener("focusin", handleFocusIn);
      window.removeEventListener("scroll", handleScroll, true);
      if (scrollFrameRef.current !== null) {
        cancelAnimationFrame(scrollFrameRef.current);
        scrollFrameRef.current = null;
      }
    };
  }, [open, positionContent, setOpen]);

  const triggerWithState = cloneElement(trigger, {
    id: triggerId,
    "aria-controls": open ? contentId : undefined,
    "aria-expanded": open,
    "aria-haspopup": "dialog",
    onClick: (event) => {
      trigger.props.onClick?.(event);
      if (!event.defaultPrevented) {
        setOpen(!open);
      }
    },
  });

  return (
    <>
      <span ref={triggerRef} className="inline-flex max-w-full min-w-0 shrink">
        {triggerWithState}
      </span>
      {open &&
        createPortal(
          <div
            id={contentId}
            ref={contentRef}
            role="dialog"
            aria-labelledby={triggerId}
            style={{
              left: position?.left ?? 0,
              top: position?.top ?? 0,
              visibility: position ? "visible" : "hidden",
            }}
            className={cn(
              "border-border bg-popover text-popover-foreground fixed z-50 max-h-[calc(100vh-1rem)] max-w-[calc(100vw-1rem)] min-w-48 overflow-y-auto rounded-xl border p-0 shadow-md",
              contentClassName,
            )}
            onClick={(event) => {
              if ((event.target as Element).closest(`[${DETAIL_POPOVER_CLOSE_ATTR}]`)) {
                setOpen(false);
              }
            }}
          >
            {children}
          </div>,
          document.body,
        )}
    </>
  );
}
