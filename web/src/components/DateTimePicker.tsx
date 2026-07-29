import { useState } from "react";
import { CalendarIcon } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Calendar } from "@/components/ui/calendar";
import { Input } from "@/components/ui/input";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { formatDateTime } from "@/lib/datetime";
import { cn } from "@/lib/utils";

/**
 * Calendar + time-of-day picker that speaks the same `YYYY-MM-DDTHH:mm` local
 * string format as a native `datetime-local` input, so it can drop into
 * existing filter state without conversions.
 */
export function DateTimePicker({
  id,
  value,
  onChange,
  placeholder = "Any time",
  className,
}: {
  id?: string;
  value: string;
  onChange: (value: string) => void;
  placeholder?: string;
  className?: string;
}) {
  const [open, setOpen] = useState(false);
  const selected = parseLocalDateTime(value);
  const time = selected ? value.slice(11, 16) : "";

  function handleSelectDay(day: Date | undefined) {
    if (!day) {
      onChange("");
      return;
    }
    onChange(`${toLocalDateString(day)}T${time || "00:00"}`);
  }

  function handleTimeChange(nextTime: string) {
    const day = selected ?? new Date();
    onChange(`${toLocalDateString(day)}T${nextTime || "00:00"}`);
  }

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button
          id={id}
          type="button"
          variant="outline"
          className={cn(
            "w-full justify-start bg-transparent px-3 font-normal",
            !selected && "text-muted-foreground",
            className,
          )}
        >
          <CalendarIcon className="size-4 opacity-60" aria-hidden="true" />
          <span className="truncate">
            {selected ? formatDateTime(selected, { seconds: false }) : placeholder}
          </span>
        </Button>
      </PopoverTrigger>
      <PopoverContent className="w-auto p-0" align="start">
        <Calendar
          mode="single"
          selected={selected}
          defaultMonth={selected}
          onSelect={handleSelectDay}
        />
        <div className="flex items-center gap-2 border-t p-3">
          <Input
            type="time"
            aria-label="Time of day"
            value={time}
            onChange={(event) => handleTimeChange(event.target.value)}
            className="h-8"
          />
          <Button
            type="button"
            variant="ghost"
            size="sm"
            disabled={!selected}
            onClick={() => onChange("")}
          >
            Clear
          </Button>
        </div>
      </PopoverContent>
    </Popover>
  );
}

function parseLocalDateTime(value: string) {
  if (!value) return undefined;
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? undefined : date;
}

function toLocalDateString(day: Date) {
  const pad = (part: number) => String(part).padStart(2, "0");
  return `${day.getFullYear()}-${pad(day.getMonth() + 1)}-${pad(day.getDate())}`;
}
