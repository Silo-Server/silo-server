import { useId } from "react";
import type { RequestOption } from "@/api/types";
import type { RequestOptionPicks } from "@/lib/mediaRequests";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";

interface RequestOptionFieldsProps {
  options: readonly RequestOption[];
  picks: RequestOptionPicks;
  onChange: (picks: RequestOptionPicks) => void;
  disabled?: boolean;
}

/**
 * One select per download server setting the viewer may choose for a request,
 * such as a quality profile. The labels come from the server's request plugin.
 */
export function RequestOptionFields({
  options,
  picks,
  onChange,
  disabled,
}: RequestOptionFieldsProps) {
  const baseID = useId();
  if (options.length === 0) return null;
  return (
    <div className="space-y-3">
      {options.map((option) => {
        const id = `${baseID}-${option.key}`;
        const picked = picks[option.key];
        const value =
          picked !== undefined && option.choices.some((choice) => choice.value === picked)
            ? picked
            : option.default;
        return (
          <div key={option.key} className="space-y-1.5">
            <label htmlFor={id} className="text-sm font-medium">
              {option.label}
            </label>
            <Select
              value={value}
              onValueChange={(next) => onChange({ ...picks, [option.key]: next })}
              disabled={disabled}
            >
              <SelectTrigger id={id} className="w-full min-w-0">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {option.choices.map((choice) => (
                  <SelectItem key={choice.value} value={choice.value}>
                    {choice.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        );
      })}
    </div>
  );
}
