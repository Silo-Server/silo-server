import { useId, type ReactNode } from "react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useT } from "@/i18n";
import { cn } from "@/lib/utils";
import { SETTINGS_CONTROL_WIDTH, SettingFieldRow } from "@/pages/admin-settings/SettingField";

/** Placeholder standing in for a saved secret. */
const SAVED_SECRET_MASK = "••••••••••••";

export interface SecretFieldProps {
  label: string;
  /** Staged plaintext value; empty while the saved secret is kept. */
  value: string;
  /** Whether the server already stores a value for this key. */
  configured: boolean;
  onChange: (value: string) => void;
  /**
   * Called when the input is emptied while a saved value exists, so a
   * settings-form parent can revert the staged draft (`form.resetValue`)
   * instead of staging `""` — a dirty `""` would clear the secret on save.
   * Draft-based parents that already skip empty values may omit it.
   */
  onKeep?: () => void;
  /**
   * Stages clearing the saved secret, normally `form.setValue(key, "")` so the
   * page's save bar writes the empty value with the rest of the batch. Pass it
   * only where the surface has no clear action of its own; pages that own one
   * (Disconnect, Clear credentials) keep a single clear per surface.
   */
  onClear?: () => void;
  /**
   * Whether the parent currently has a clear staged for this key, e.g.
   * `form.isDirty(key) && form.getValue(key) === ""`. Only meaningful
   * alongside `onClear`.
   */
  cleared?: boolean;
  hint?: string;
  /** Extra line under the description, e.g. a save error for this field. */
  status?: ReactNode;
  disabled?: boolean;
  restartRequired?: boolean;
}

/**
 * The single credential control for admin settings: one always-editable
 * password input. A saved secret shows as a masked placeholder, typing stages
 * a replacement, and emptying the input keeps the saved value, so no ordinary
 * save can erase a secret by accident.
 *
 * Clearing one is always deliberate, and comes from exactly one of two places:
 * a page-level action (Disconnect, Clear credentials), or — on surfaces that
 * have none — this field's own opt-in `onClear` affordance, which stages the
 * empty write for the page's save bar and can be taken back with "Keep saved
 * value" or Discard.
 */
export function SecretField({
  label,
  value,
  configured,
  onChange,
  onKeep,
  onClear,
  cleared = false,
  hint,
  status,
  disabled = false,
  restartRequired = false,
}: SecretFieldProps) {
  const t = useT();
  const controlId = useId();
  const hintId = useId();

  // A typed replacement outranks both other states: it is neither a keep nor a
  // clear, so the action disappears until the input is empty again.
  const clearStaged = cleared && value === "";
  const showAction = onClear != null && configured && !disabled && value === "";

  // While a clear is staged its description replaces `hint`.
  const description = clearStaged
    ? t("web.settings.secretField.clearedDescription")
    : (hint ?? (configured ? t("web.settings.secretField.configuredDescription") : undefined));

  function keepSaved() {
    if (onKeep) onKeep();
    else onChange("");
  }

  function handleChange(next: string) {
    if (next === "" && configured) {
      // Emptying the field means "keep the saved secret", never "clear it".
      keepSaved();
      return;
    }
    onChange(next);
  }

  return (
    <SettingFieldRow
      label={label}
      htmlFor={controlId}
      description={description}
      descriptionId={hintId}
      status={status}
      restartRequired={restartRequired}
    >
      {/* The action sits ahead of the input, as in LimitField: the row's unit
          slot is to the right of the control edge, so anything between the
          input and that edge would knock this field out of line with the
          plain rows it is stacked against. */}
      <div className="flex w-full flex-wrap items-center justify-end gap-x-3 gap-y-2 sm:w-auto">
        {showAction ? (
          <Button
            type="button"
            size="sm"
            // Outline, never ghost: a clear must carry a border at rest rather
            // than appear only on hover.
            variant="outline"
            onClick={clearStaged ? keepSaved : onClear}
          >
            {clearStaged ? t("web.settings.secretField.keep") : t("web.settings.secretField.clear")}
          </Button>
        ) : null}
        <Input
          id={controlId}
          type="password"
          placeholder={
            clearStaged
              ? t("web.settings.secretField.willClear")
              : configured
                ? SAVED_SECRET_MASK
                : t("web.settings.secretField.notConfigured")
          }
          value={value}
          onChange={(e) => handleChange(e.target.value)}
          disabled={disabled}
          className={cn("border-muted-foreground/25", SETTINGS_CONTROL_WIDTH)}
          aria-describedby={description ? hintId : undefined}
        />
      </div>
    </SettingFieldRow>
  );
}
