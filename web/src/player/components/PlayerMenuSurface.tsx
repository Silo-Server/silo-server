import { useEffect, type KeyboardEvent, type ReactNode, type RefObject } from "react";
import { useCoarsePointer } from "../hooks/useCoarsePointer";

interface PlayerMenuSurfaceProps {
  children: ReactNode;
  className: string;
  onClose: () => void;
  onKeyDown?: (event: KeyboardEvent<HTMLDivElement>) => void;
  /**
   * The menu's trigger wrapper. A pointer press outside it closes the menu.
   * Focus-based dismissal alone is not enough: Safari does not focus buttons
   * on click, so the trigger never blurs.
   */
  anchorRef?: RefObject<HTMLElement | null>;
}

export function PlayerMenuSurface({
  children,
  className,
  onClose,
  onKeyDown,
  anchorRef,
}: PlayerMenuSurfaceProps) {
  const isCoarsePointer = useCoarsePointer();

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

  if (!isCoarsePointer) {
    return (
      <div
        role="menu"
        className={`player-menu-surface ${className}`}
        onKeyDown={onKeyDown}
        onClick={(event) => event.stopPropagation()}
      >
        {children}
      </div>
    );
  }

  return (
    <>
      <button
        type="button"
        className="fixed inset-0 z-40 bg-black/50"
        aria-label="Close menu"
        onClick={(event) => {
          event.stopPropagation();
          onClose();
        }}
      />
      <div
        role="menu"
        className="fixed inset-x-0 bottom-0 z-50 max-h-[70dvh] overflow-y-auto rounded-t-2xl bg-black/90 pt-2 pb-[max(0.75rem,env(safe-area-inset-bottom))] shadow-2xl backdrop-blur"
        onKeyDown={onKeyDown}
        onClick={(event) => event.stopPropagation()}
      >
        {children}
      </div>
    </>
  );
}
