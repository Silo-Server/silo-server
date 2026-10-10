import { useRef } from "react";

/**
 * Focus handlers for a dialog opened without a Radix trigger (from a menu
 * item or a button elsewhere): focus goes back to whatever had it when the
 * dialog opened, instead of the page body. A dialog whose opener goes away
 * (a deleted row's ⋯) can skip that with `skip`.
 */
export function useReturnFocus(skip?: () => boolean) {
  const opener = useRef<HTMLElement | null>(null);
  return {
    onOpenAutoFocus: () => {
      const active = document.activeElement;
      opener.current = active instanceof HTMLElement ? active : null;
    },
    onCloseAutoFocus: (event: Event) => {
      event.preventDefault();
      if (!skip?.()) opener.current?.focus();
      opener.current = null;
    },
  };
}
