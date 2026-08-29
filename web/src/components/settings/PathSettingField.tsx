import { useId, type ReactNode } from "react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { SETTINGS_CONTROL_WIDTH, SettingFieldRow } from "@/pages/admin-settings/SettingField";
import { cn } from "@/lib/utils";

export interface PathSettingFieldProps {
  label: string;
  /**
   * What the server runs while the field is blank, e.g. `/tmp/silo-transcode`.
   * It is shown as the placeholder and is what "Reset to default" restores, so
   * it has to be the real effective value — see
   * `@/pages/admin-settings/settingsPathDefaults`.
   */
  defaultValue: string;
  /** Sentence under the label. Say what blank means, in words. */
  description?: ReactNode;
  value: string;
  onChange: (value: string) => void;
  /** Marks the field with a restart badge; drive it from `useRestartKeys`. */
  restartRequired?: boolean;
}

/**
 * A settings row for a filesystem path whose stored value may be blank, where
 * blank is a real choice ("follow the server's rule") rather than "unset".
 *
 * Two things follow from that, and neither works on a plain text field: the
 * placeholder has to name the path blank resolves to, and an admin who typed an
 * override needs a way back that does not require knowing what the built-in
 * value was. Reset stages an empty string through the normal form flow, so the
 * save bar confirms it like any other edit.
 */
export function PathSettingField({
  label,
  defaultValue,
  description,
  value,
  onChange,
  restartRequired,
}: PathSettingFieldProps) {
  const controlId = useId();
  const descriptionId = useId();
  // Nothing to reset while the field already runs the default, whether it is
  // blank or holds the default path verbatim: clearing it would save a row
  // nobody could see the effect of.
  const overridden = value !== "" && value !== defaultValue;

  return (
    <SettingFieldRow
      label={label}
      htmlFor={controlId}
      description={description}
      descriptionId={descriptionId}
      restartRequired={restartRequired}
    >
      {/* The action sits under the control rather than beside it: the row
          reserves a unit slot to the right of every control, so anything
          between the input and that slot would knock this field's box out of
          line with the rows stacked against it. */}
      <div className="flex w-full flex-col items-stretch gap-1 sm:w-auto sm:items-end">
        <Input
          id={controlId}
          type="text"
          value={value}
          placeholder={defaultValue}
          onChange={(e) => onChange(e.target.value)}
          className={cn("border-muted-foreground/25", SETTINGS_CONTROL_WIDTH)}
          aria-describedby={description ? descriptionId : undefined}
        />
        {overridden ? (
          <Button
            type="button"
            variant="ghost"
            size="xs"
            // Several of these rows can share a page, so the label names the
            // field instead of leaving a screen reader with three identical
            // "Reset to default" buttons.
            aria-label={`Reset ${label} to default`}
            className="text-muted-foreground hover:text-foreground -mr-2 self-end"
            onClick={() => onChange("")}
          >
            Reset to default
          </Button>
        ) : null}
      </div>
    </SettingFieldRow>
  );
}
