import { useId } from "react";
import { Switch } from "@/components/ui/switch";
import { Label } from "@/components/ui/label";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";

interface SelectOption {
  value: string;
  label: string;
}

interface SettingFieldProps {
  label: string;
  type?: "text" | "number" | "password" | "toggle" | "duration" | "select";
  hint?: string;
  value: string;
  onChange: (value: string) => void;
  options?: SelectOption[];
  sensitiveConfigured?: boolean;
  disabled?: boolean;
}

export function SettingField({
  label,
  type = "text",
  hint,
  value,
  onChange,
  options,
  sensitiveConfigured,
  disabled,
}: SettingFieldProps) {
  const controlId = useId();
  const hintId = useId();

  if (type === "toggle") {
    const checked = value === "true";
    return (
      <div className="flex flex-col justify-between gap-3 py-3 sm:flex-row sm:items-center">
        <div className="space-y-0.5">
          <Label htmlFor={controlId} className="text-sm font-medium">
            {label}
          </Label>
          {hint && (
            <p id={hintId} className="text-muted-foreground text-xs">
              {hint}
            </p>
          )}
        </div>
        <Switch
          id={controlId}
          checked={checked}
          onCheckedChange={(val) => onChange(val ? "true" : "false")}
          disabled={disabled}
          aria-describedby={hint ? hintId : undefined}
        />
      </div>
    );
  }

  if (type === "select" && options) {
    const currentVal = value || options[0]?.value || "";
    return (
      <div className="space-y-1 py-2">
        <Label htmlFor={controlId} className="text-sm font-medium">
          {label}
        </Label>
        <Select value={currentVal} onValueChange={onChange} disabled={disabled}>
          <SelectTrigger
            id={controlId}
            className="w-full sm:w-fit"
            aria-describedby={hint ? hintId : undefined}
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {options.map((opt) => (
              <SelectItem key={opt.value} value={opt.value}>
                {opt.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        {hint && (
          <p id={hintId} className="text-muted-foreground text-xs">
            {hint}
          </p>
        )}
      </div>
    );
  }

  if (type === "password") {
    const placeholder = sensitiveConfigured ? "configured" : (hint ?? "Not configured");
    return (
      <div className="space-y-1 py-2">
        <Label htmlFor={controlId} className="text-sm font-medium">
          {label}
        </Label>
        <Input
          id={controlId}
          type="password"
          placeholder={placeholder}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          disabled={disabled}
          className="max-w-md"
          aria-describedby={hint ? hintId : undefined}
        />
        {hint && (
          <p id={hintId} className="text-muted-foreground text-xs">
            {hint}
          </p>
        )}
      </div>
    );
  }

  if (type === "number") {
    return (
      <div className="space-y-1 py-2">
        <Label htmlFor={controlId} className="text-sm font-medium">
          {label}
        </Label>
        <Input
          id={controlId}
          type="number"
          value={value}
          onChange={(e) => onChange(e.target.value)}
          disabled={disabled}
          className="w-full sm:w-40"
          aria-describedby={hint ? hintId : undefined}
        />
        {hint && (
          <p id={hintId} className="text-muted-foreground text-xs">
            {hint}
          </p>
        )}
      </div>
    );
  }

  // text and duration
  return (
    <div className="space-y-1 py-2">
      <Label htmlFor={controlId} className="text-sm font-medium">
        {label}
      </Label>
      <Input
        id={controlId}
        type="text"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        disabled={disabled}
        className="max-w-md"
        placeholder={sensitiveConfigured ? "configured" : hint}
        aria-describedby={hint && type === "duration" ? hintId : undefined}
      />
      {hint && type === "duration" && (
        <p id={hintId} className="text-muted-foreground text-xs">
          {hint}
        </p>
      )}
    </div>
  );
}
