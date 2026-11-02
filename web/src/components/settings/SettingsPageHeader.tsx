import { useContext, type ReactNode } from "react";

import { PageHeader } from "@/components/page/PageHeader";
import { SettingsPageDescriptionContext } from "@/components/settings/settingsPageDescription";
import "@/styles/admin-settings.css";

export interface SettingsPageHeaderProps {
  title: string;
  /** Right-aligned page actions, level with the title. */
  actions?: ReactNode;
  className?: string;
}

/** The heading every admin settings page opens with: the shared `PageHeader`. */
export function SettingsPageHeader({ title, actions, className }: SettingsPageHeaderProps) {
  const description = useContext(SettingsPageDescriptionContext);
  return (
    <PageHeader title={title} subtitle={description} actions={actions} className={className} />
  );
}
