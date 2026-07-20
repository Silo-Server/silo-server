import { Button } from "@/components/ui/button";
import { READER_SHORTCUTS } from "./readerNavigation";

export type ReaderShortcutsOverlayProps = {
  onClose: () => void;
};

export default function ReaderShortcutsOverlay({ onClose }: ReaderShortcutsOverlayProps) {
  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-label="Keyboard shortcuts"
      className="fixed inset-0 z-40 flex items-center justify-center bg-black/50"
      onClick={onClose}
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
