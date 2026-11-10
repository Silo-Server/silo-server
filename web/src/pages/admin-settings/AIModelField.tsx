import { useId, useRef, type ReactNode } from "react";

import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
} from "@/components/ui/select";
import { useT } from "@/i18n";
import { useAIProviderModels, type AICapability } from "@/hooks/queries/admin/aiProviders";
import { cn } from "@/lib/utils";
import { SETTINGS_CONTROL_WIDTH, SettingFieldRow } from "./SettingField";

// ---------------------------------------------------------------------------
// Model field
// ---------------------------------------------------------------------------

/**
 * A model name with the selected provider's listed models as suggestions. The
 * list is a convenience: providers that do not list models, or list them
 * without saying which are which, still take any typed name.
 */
export function ModelField({
  label,
  description,
  providerID,
  kind,
  value,
  onChange,
  onCommit,
  placeholder,
}: {
  label: string;
  description?: ReactNode;
  providerID: string;
  kind: AICapability;
  value: string;
  onChange: (value: string) => void;
  /** Called when focus leaves the field, for pages that save each field. */
  onCommit?: (value: string) => void;
  placeholder?: string;
}) {
  const t = useT();
  const inputID = useId();
  const inputRef = useRef<HTMLInputElement>(null);
  const pickerOpen = useRef(false);
  const models = useAIProviderModels(providerID);
  const suggestions = (models.data?.models ?? []).filter((model) => model.kind === kind);
  const status =
    providerID === "" ? undefined : models.isError ? (
      <span className="text-muted-foreground text-xs">
        {t("web.admin.settings.ai.modelField.loadFailed")}
      </span>
    ) : suggestions.length > 0 ? (
      <span className="text-muted-foreground text-xs">
        {t("web.admin.settings.ai.modelField.suggested", {
          count: suggestions.length,
          count_text: String(suggestions.length),
        })}
      </span>
    ) : undefined;
  return (
    <SettingFieldRow label={label} htmlFor={inputID} description={description} status={status}>
      <div
        className={cn(SETTINGS_CONTROL_WIDTH, "flex min-w-0 items-center gap-1")}
        onBlur={(event) => {
          if (!pickerOpen.current && !event.currentTarget.contains(event.relatedTarget)) {
            onCommit?.(value);
          }
        }}
      >
        <Input
          ref={inputRef}
          id={inputID}
          value={value}
          onChange={(event) => onChange(event.target.value)}
          placeholder={placeholder}
          autoComplete="off"
          spellCheck={false}
          className="font-mono text-[13px]"
        />
        {suggestions.length > 0 && (
          <Select
            value={suggestions.some((model) => model.id === value) ? value : ""}
            onOpenChange={(open) => {
              pickerOpen.current = open;
            }}
            onValueChange={onChange}
          >
            <SelectTrigger
              aria-label={t("web.admin.settings.ai.modelField.choose")}
              className="size-9 shrink-0 justify-center px-0"
            />
            <SelectContent
              position="popper"
              align="end"
              onCloseAutoFocus={(event) => {
                event.preventDefault();
                inputRef.current?.focus();
              }}
            >
              <SelectGroup>
                {suggestions.map((model) => (
                  <SelectItem key={model.id} value={model.id}>
                    {model.id}
                  </SelectItem>
                ))}
              </SelectGroup>
            </SelectContent>
          </Select>
        )}
      </div>
    </SettingFieldRow>
  );
}
