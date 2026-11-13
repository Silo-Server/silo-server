import { CircleAlert, GripVertical, Info } from "lucide-react";
import type { ReactNode } from "react";
import { Button } from "@/components/ui/button";
import { useT } from "@/i18n";

/** Shown when the rows changed under this page (a 412, or a row that moved since it loaded). */
export function ConflictBanner({ onReload, busy }: { onReload: () => void; busy?: boolean }) {
  const t = useT();
  return (
    <div
      role="alert"
      className="border-warning/40 bg-warning/10 flex items-center gap-3 rounded-2xl border px-4 py-3 text-sm"
    >
      <CircleAlert aria-hidden className="text-warning size-[18px] shrink-0" />
      <p className="min-w-0 flex-1">{t("web.home.rows.notes.conflict")}</p>
      <Button size="sm" variant="outline" disabled={busy} onClick={onReload}>
        {t("web.home.rows.reloadRows")}
      </Button>
    </div>
  );
}

export function LibraryPageNote({ libraryName }: { libraryName: string }) {
  const t = useT();
  return (
    <p className="text-muted-foreground -mt-3 flex items-center gap-2 text-[13px]">
      <Info aria-hidden className="size-[15px] shrink-0" />
      {t("web.home.rows.notes.libraryPage", { library: libraryName })}
    </p>
  );
}

function Key({ children, label }: { children: ReactNode; label?: string }) {
  return (
    <kbd
      aria-label={label}
      className="border-border bg-background text-foreground inline-grid h-5 min-w-5 place-items-center rounded-md border px-1.5 text-xs font-medium"
    >
      {children}
    </kbd>
  );
}

export function ReorderHint() {
  const t = useT();
  return (
    <p className="text-muted-foreground flex items-start gap-2 px-[18px] pt-3 pb-3.5 text-[13px] leading-6">
      <GripVertical aria-hidden className="mt-1 size-[15px] shrink-0" />
      <span>
        {t("web.home.rows.notes.reorderHint", {
          space: (chunks) => <Key>{chunks}</Key>,
          up: (chunks) => <Key label={t("web.home.rows.notes.upArrow")}>{chunks}</Key>,
          down: (chunks) => <Key label={t("web.home.rows.notes.downArrow")}>{chunks}</Key>,
        })}
      </span>
    </p>
  );
}
