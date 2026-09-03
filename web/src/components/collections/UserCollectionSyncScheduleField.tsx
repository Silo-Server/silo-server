import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";

import { SyncScheduleField } from "./SyncScheduleField";
import {
  BOUNDED_SCHEDULE_OPTIONS,
  MANUAL_SCHEDULE,
  adminScheduleValue,
  boundedScheduleChoice,
} from "@/lib/userCollectionSyncSchedule";

interface Props {
  value: string;
  onChange: (value: string) => void;
  allowCustomCron: boolean;
  disabled?: boolean;
  label?: string;
  inputId?: string;
}

export function UserCollectionSyncScheduleField({
  value,
  onChange,
  allowCustomCron,
  disabled,
  label = "Auto Refresh",
  inputId,
}: Props) {
  if (allowCustomCron) {
    return (
      <SyncScheduleField
        value={adminScheduleValue(value)}
        onChange={onChange}
        disabled={disabled}
        label={label}
        inputId={inputId}
      />
    );
  }

  const choice = boundedScheduleChoice(value);
  return (
    <div className="space-y-2">
      <Label htmlFor={inputId}>{label}</Label>
      <Select
        value={choice}
        onValueChange={(next) => onChange(next === MANUAL_SCHEDULE ? "" : next)}
        disabled={disabled}
      >
        <SelectTrigger id={inputId} className="w-full">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {BOUNDED_SCHEDULE_OPTIONS.map((option) => (
            <SelectItem key={option.value} value={option.value}>
              {option.label}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </div>
  );
}
