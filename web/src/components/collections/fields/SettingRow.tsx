import { useId, type ReactNode } from "react";

/**
 * One row of a settings list, styled like `ToggleRow`: its name, the value
 * under it, an action on the right, and anything longer (a list) across the
 * full width underneath. A labelled group unless `group` is false.
 */
export function SettingRow({
  label,
  value,
  action,
  children,
  group = true,
}: {
  label: string;
  value?: ReactNode;
  action?: ReactNode;
  children?: ReactNode;
  group?: boolean;
}) {
  const id = useId();
  return (
    <div
      role={group ? "group" : undefined}
      aria-labelledby={group ? `${id}-label` : undefined}
      className="grid gap-3"
    >
      <div className="flex items-center justify-between gap-4">
        <div className="min-w-0">
          <h3 id={`${id}-label`} className="text-[14.5px] font-semibold">
            {label}
          </h3>
          {value ? (
            <div className="text-muted-foreground mt-0.5 text-[13px] leading-snug">{value}</div>
          ) : null}
        </div>
        {action ? <div className="flex shrink-0 items-center gap-2">{action}</div> : null}
      </div>
      {children}
    </div>
  );
}
