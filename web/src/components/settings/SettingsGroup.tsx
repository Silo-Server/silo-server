import { useId, type ReactNode } from "react";

import { cn } from "@/lib/utils";

interface SettingsGroupProps {
  title: string;
  description?: ReactNode;
  /** Right-aligned controls level with the title. */
  actions?: ReactNode;
  /**
   * Content rows that draw their own padding and hairlines (admin
   * `SettingFieldRow`s) instead of spaced blocks.
   */
  flush?: boolean;
  children: ReactNode;
  className?: string;
}

/**
 * The one settings panel: a quiet surface with a small title, shared by the
 * user settings pages and (via the admin `FieldGroup` wrapper) the admin
 * settings pages, so both surfaces read as the same product.
 */
export function SettingsGroup({
  title,
  description,
  actions,
  flush = false,
  children,
  className,
}: SettingsGroupProps) {
  const titleId = useId();
  return (
    <section
      role="group"
      aria-labelledby={titleId}
      className={cn("surface-panel-raised px-4 py-5 sm:px-6", className)}
    >
      <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1">
        <div className="min-w-0 space-y-1">
          <h3 id={titleId} className="text-foreground text-sm font-semibold tracking-tight">
            {title}
          </h3>
          {description ? (
            <p className="text-muted-foreground text-[13px] leading-relaxed">{description}</p>
          ) : null}
        </div>
        {actions ? <div className="flex shrink-0 items-center gap-2">{actions}</div> : null}
      </div>
      <div className={flush ? "mt-2" : "mt-5 space-y-4"}>{children}</div>
    </section>
  );
}
