import { createContext, useContext, type ReactNode } from "react";

import { SettingsGroup } from "@/components/settings/SettingsGroup";
import { cn } from "@/lib/utils";
import "@/styles/admin-settings.css";

const GroupRestartContext = createContext(false);

/** True inside a `FieldGroup` where every field only applies after a restart. */
export function useGroupRestartAll(): boolean {
  return useContext(GroupRestartContext);
}

export interface FieldGroupProps {
  /** Sentence-case heading, e.g. "Transcoding". */
  label: string;
  /** One sentence on what the group holds, shown under the heading. */
  description?: string;
  /**
   * Every field in the group only takes effect after a restart. The group says
   * so once and the fields inside drop their own chips.
   */
  restartAll?: boolean;
  /** At least one field in the group has an unsaved edit. */
  dirty?: boolean;
  /** Right-aligned controls on the heading line. */
  actions?: ReactNode;
  className?: string;
  children: ReactNode;
}

/**
 * An admin settings group: the shared `SettingsGroup` panel (the same surface
 * the user settings pages use) holding hairline-ruled `SettingFieldRow`s. The
 * admin-only concerns layer on top: the restart-all line, the unsaved-edits
 * dot, and the restart context the rows read to drop their own chips.
 */
export function FieldGroup({
  label,
  description,
  restartAll = false,
  dirty = false,
  actions,
  className,
  children,
}: FieldGroupProps) {
  // A group nested inside a restart-all group inherits that state without
  // repeating the line.
  const inheritedRestartAll = useContext(GroupRestartContext);
  const effectiveRestartAll = restartAll || inheritedRestartAll;

  const dirtyDot = dirty ? (
    <span className="inline-flex items-center" title="Unsaved changes in this group">
      <span aria-hidden="true" className="size-1.5 rounded-full bg-[var(--settings-accent)]" />
      <span className="sr-only">Unsaved changes in this group</span>
    </span>
  ) : null;

  return (
    <SettingsGroup
      title={label}
      description={
        restartAll
          ? description
            ? `${description} Changes apply after a restart.`
            : "Changes apply after a restart"
          : description
      }
      actions={
        actions || dirtyDot ? (
          <>
            {actions}
            {dirtyDot}
          </>
        ) : undefined
      }
      flush
      className={cn("min-w-0", className)}
    >
      <GroupRestartContext.Provider value={effectiveRestartAll}>
        <div className="settings-field-list">{children}</div>
      </GroupRestartContext.Provider>
    </SettingsGroup>
  );
}
