import type { ReactNode } from "react";
import { Bug, TriangleAlert } from "lucide-react";
import { Label } from "@/components/ui/label";
import { cn } from "@/lib/utils";
import { useT } from "@/i18n";

export function FilterField({
  label,
  htmlFor,
  className,
  children,
}: {
  label: string;
  htmlFor: string;
  className?: string;
  children: ReactNode;
}) {
  return (
    <div className={cn("flex flex-col gap-1.5", className)}>
      <Label
        htmlFor={htmlFor}
        className="text-muted-foreground text-[11px] font-medium tracking-wide uppercase"
      >
        {label}
      </Label>
      {children}
    </div>
  );
}

export function FeatureStatusBanner({ status }: { status: "disabled" | "storage_unavailable" }) {
  const t = useT();
  const message =
    status === "disabled"
      ? t("web.admin.diagnostics.banner.disabled")
      : t("web.admin.diagnostics.banner.storageUnavailable");
  return (
    <div className="border-border bg-muted/30 text-muted-foreground flex items-start gap-3 rounded-xl border px-4 py-3 text-sm">
      <TriangleAlert className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
      <p>{message}</p>
    </div>
  );
}

export function EmptyState({ filtered }: { filtered: boolean }) {
  const t = useT();
  return (
    <div className="mx-auto flex max-w-md flex-col items-center gap-2">
      <div className="bg-muted text-muted-foreground flex size-10 items-center justify-center rounded-xl">
        <Bug className="size-5" aria-hidden="true" />
      </div>
      <h2 className="font-semibold">{t("web.admin.diagnostics.empty.title")}</h2>
      <p className="text-muted-foreground text-sm">
        {filtered
          ? t("web.admin.diagnostics.empty.filtered")
          : t("web.admin.diagnostics.empty.unfiltered")}
      </p>
    </div>
  );
}
