import { useEffect, useEffectEvent, useLayoutEffect, useRef, type ReactNode } from "react";
import { createPortal } from "react-dom";

const supportsClosedBy =
  typeof HTMLDialogElement !== "undefined" && "closedBy" in HTMLDialogElement.prototype;

function outsideDialog(event: React.PointerEvent<HTMLDialogElement>): boolean {
  // The backdrop's events target the dialog itself, as do its own padding and
  // gaps, so test the pointer position instead.
  if (event.target !== event.currentTarget) return false;
  const r = event.currentTarget.getBoundingClientRect();
  return (
    event.clientX < r.left ||
    event.clientX > r.right ||
    event.clientY < r.top ||
    event.clientY > r.bottom
  );
}

interface PlayerModalDialogProps {
  label: string;
  className: string;
  onClose: () => void;
  children: ReactNode;
}

/**
 * A native modal dialog for the player. showModal() puts it in the top layer,
 * above the fullscreen player, and the browser contains focus, closes it on
 * Escape, and hands focus back to the control that opened it. A control marked
 * data-autofocus takes the initial focus. Mount it to open, unmount to close.
 */
export function PlayerModalDialog({ label, className, onClose, children }: PlayerModalDialogProps) {
  const ref = useRef<HTMLDialogElement>(null);
  const pressedOutside = useRef(false);
  const unmounting = useRef(false);
  const close = useEffectEvent(onClose);

  // The browser hands focus back to whatever held it at showModal(), so the
  // caller puts focus on the opening control before mounting this.
  useEffect(() => {
    const dialog = ref.current;
    if (!dialog) return;
    dialog.showModal();
    dialog.querySelector<HTMLElement>("[data-autofocus]")?.focus();
    const onCloseEvent = () => {
      if (!dialog.open && !unmounting.current) close();
    };
    // Fullscreen entered while the dialog is open stacks the player above it;
    // reopening puts the dialog back on top with focus where it was.
    const onFullscreenChange = () => {
      if (!document.fullscreenElement || !dialog.open) return;
      const focused = document.activeElement;
      dialog.close();
      dialog.showModal();
      if (focused instanceof HTMLElement && dialog.contains(focused)) focused.focus();
    };
    dialog.addEventListener("close", onCloseEvent);
    document.addEventListener("fullscreenchange", onFullscreenChange);
    return () => {
      dialog.removeEventListener("close", onCloseEvent);
      document.removeEventListener("fullscreenchange", onFullscreenChange);
    };
  }, []);

  // Close before React removes the element, so focus goes back to the opener
  // when the parent unmounts the dialog (after a download, for example).
  useLayoutEffect(() => {
    const dialog = ref.current;
    unmounting.current = false;
    return () => {
      // The parent already closed it; the close event must not report back.
      unmounting.current = true;
      if (dialog?.open) dialog.close();
    };
  }, []);

  return createPortal(
    <dialog
      ref={ref}
      aria-label={label}
      closedby="any"
      className={`m-auto overscroll-contain p-0 backdrop:overflow-hidden backdrop:overscroll-contain backdrop:bg-black/80 ${className}`}
      onPointerDown={
        supportsClosedBy ? undefined : (e) => (pressedOutside.current = outsideDialog(e))
      }
      onPointerUp={
        supportsClosedBy
          ? undefined
          : (e) => {
              // A drag that only ends on the backdrop, like a text selection,
              // does not dismiss.
              if (pressedOutside.current && outsideDialog(e)) e.currentTarget.close();
              pressedOutside.current = false;
            }
      }
    >
      {children}
    </dialog>,
    document.body,
  );
}
