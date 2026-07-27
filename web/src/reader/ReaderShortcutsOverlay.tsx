import { useRef, type KeyboardEvent } from "react";
import { Button } from "@/components/ui/button";
import { READER_SHORTCUTS } from "./readerNavigation";

export type ReaderShortcutsOverlayProps = {
  onClose: () => void;
};

const FOCUSABLE_SELECTOR =
  'a[href], button:not([disabled]), textarea, input, select, [tabindex]:not([tabindex="-1"])';

export default function ReaderShortcutsOverlay({ onClose }: ReaderShortcutsOverlayProps) {
  const dialogRef = useRef<HTMLDivElement>(null);

  // Keeps Tab/Shift+Tab focus cycling within the dialog instead of escaping
  // into the page behind it. Written generically over whatever is focusable
  // inside at the time — today that's just the Close button, so Tab simply
  // keeps focus there, but this keeps working unchanged if more controls are
  // added later.
  const trapTabFocus = (event: KeyboardEvent<HTMLDivElement>) => {
    if (event.key !== "Tab") return;
    const focusable = Array.from(
      dialogRef.current?.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR) ?? [],
    );
    const first = focusable[0];
    const last = focusable[focusable.length - 1];
    if (!first || !last) return;
    const active = document.activeElement;
    const atEdge = event.shiftKey ? active === first : active === last;
    const outside = !focusable.includes(active as HTMLElement);
    if (atEdge || outside) {
      event.preventDefault();
      (event.shiftKey ? last : first).focus();
    }
  };

  return (
    <div
      ref={dialogRef}
      role="dialog"
      aria-modal="true"
      aria-label="Keyboard shortcuts"
      className="fixed inset-0 z-40 flex items-center justify-center bg-black/50"
      onClick={onClose}
      onKeyDown={trapTabFocus}
    >
      <div
        className="bg-background border-border w-80 rounded-lg border p-4 shadow-lg"
        onClick={(event) => event.stopPropagation()}
      >
        <h2 className="mb-3 text-sm font-semibold">Keyboard shortcuts</h2>
        <dl className="space-y-1.5 text-sm">
          {READER_SHORTCUTS.map((shortcut) => (
            <div key={shortcut.key} className="flex justify-between gap-4">
              <dt className="text-muted-foreground">{shortcut.description}</dt>
              <dd className="font-mono">{shortcut.key}</dd>
            </div>
          ))}
        </dl>
        <Button className="mt-4 w-full" variant="outline" size="sm" onClick={onClose} autoFocus>
          Close
        </Button>
      </div>
    </div>
  );
}
