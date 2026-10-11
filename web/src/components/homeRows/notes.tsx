import { CircleAlert, GripVertical, Info } from "lucide-react";
import { Button } from "@/components/ui/button";

/** Shown when the rows changed under this page (a 412, or a row that moved since it loaded). */
export function ConflictBanner({ onReload, busy }: { onReload: () => void; busy?: boolean }) {
  return (
    <div
      role="alert"
      className="border-warning/40 bg-warning/10 flex items-center gap-3 rounded-2xl border px-4 py-3 text-sm"
    >
      <CircleAlert aria-hidden className="text-warning size-[18px] shrink-0" />
      <p className="min-w-0 flex-1">
        These rows changed since you opened this page. Reload to see the current rows, then try
        again.
      </p>
      <Button size="sm" variant="outline" disabled={busy} onClick={onReload}>
        Reload rows
      </Button>
    </div>
  );
}

export function LibraryPageNote({ libraryName }: { libraryName: string }) {
  return (
    <p className="text-muted-foreground -mt-3 flex items-center gap-2 text-[13px]">
      <Info aria-hidden className="size-[15px] shrink-0" />
      These rows show above the full {libraryName} grid.
    </p>
  );
}

function Key({ children, label }: { children: string; label?: string }) {
  return (
    <kbd
      aria-label={label}
      className="border-border bg-background text-foreground inline-grid h-5 min-w-5 place-items-center rounded-md border px-1.5 text-[11px] font-medium"
    >
      {children}
    </kbd>
  );
}

export function ReorderHint() {
  return (
    <p className="text-muted-foreground flex items-start gap-2 px-[18px] pt-3 pb-3.5 text-[13px] leading-6">
      <GripVertical aria-hidden className="mt-1 size-[15px] shrink-0" />
      <span>
        Drag a row to move it, or focus its handle and press <Key>Space</Key> then{" "}
        <Key label="Up arrow">↑</Key> <Key label="Down arrow">↓</Key>. Changes save right away.
      </span>
    </p>
  );
}
