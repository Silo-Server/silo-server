import type { ReactNode } from "react";

import { cn } from "@/lib/utils";
import "@/styles/admin-settings.css";

export interface SettingsPageHeaderProps {
  title: string;
  /** Right-aligned page actions, level with the title. */
  actions?: ReactNode;
  className?: string;
}

/**
 * The heading every admin settings page opens with: the page's name and
 * nothing else.
 */
export function SettingsPageHeader({ title, actions, className }: SettingsPageHeaderProps) {
  return (
    <header className={cn("flex min-w-0 flex-wrap items-start justify-between gap-3", className)}>
      <h1 className="text-[28px] leading-tight font-semibold tracking-[-0.03em]">{title}</h1>
      {actions ? <div className="flex shrink-0 items-center gap-2">{actions}</div> : null}
    </header>
  );
}
