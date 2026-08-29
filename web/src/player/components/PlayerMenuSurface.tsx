import { useEffect, type KeyboardEvent, type ReactNode } from "react";
import { useCoarsePointer } from "../hooks/useCoarsePointer";

interface PlayerMenuSurfaceProps {
  children: ReactNode;
  className: string;
  onClose: () => void;
  onKeyDown?: (event: KeyboardEvent<HTMLDivElement>) => void;
}

export function PlayerMenuSurface({
  children,
  className,
  onClose,
  onKeyDown,
}: PlayerMenuSurfaceProps) {
  const isCoarsePointer = useCoarsePointer();

  useEffect(() => {
    if (!isCoarsePointer) return;
    const handleKeyDown = (event: globalThis.KeyboardEvent) => {
      if (event.key === "Escape") onClose();
    };
    document.addEventListener("keydown", handleKeyDown);
    return () => document.removeEventListener("keydown", handleKeyDown);
  }, [isCoarsePointer, onClose]);

  if (!isCoarsePointer) {
    return (
      <div role="menu" className={className} onKeyDown={onKeyDown}>
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
