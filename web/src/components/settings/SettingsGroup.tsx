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
 * The one settings section, shared by the user settings pages and (via the
 * admin `FieldGroup` wrapper) the admin settings pages, so both surfaces read
 * as the same product. Sections sit on the page itself, divided by hairlines
 * rather than drawn as cards. Once the section is wide enough its title and
 * description move into a left column beside the settings, which uses the
 * width a wide screen has instead of leaving it empty. Only from `lg`, where
 * the sidebar appears: below it a section can be just as wide, and would go
 * two-column, then back to one as the sidebar took its width.
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
      className={cn("settings-section @container/settings-section", className)}
    >
      <div className="grid gap-x-12 gap-y-4 lg:@[52rem]/settings-section:grid-cols-[16rem_minmax(0,1fr)]">
        <div className="min-w-0 space-y-1.5">
          <div className="flex items-baseline justify-between gap-3">
            <h3 id={titleId} className="text-foreground text-[15px] font-semibold tracking-tight">
              {title}
            </h3>
            {actions ? <div className="flex shrink-0 items-center gap-2">{actions}</div> : null}
          </div>
          {description ? (
            <p className="text-muted-foreground text-[13px] leading-relaxed">{description}</p>
          ) : null}
        </div>
        <div
          className={cn("min-w-0", flush ? "lg:@[52rem]/settings-section:-mt-3.5" : "space-y-4")}
        >
          {children}
        </div>
      </div>
    </section>
  );
}
