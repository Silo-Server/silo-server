import * as React from "react";
import { RadioGroup as RadioGroupPrimitive } from "radix-ui";

import { cn } from "@/lib/utils";

function RadioGroup({
  className,
  ...props
}: React.ComponentProps<typeof RadioGroupPrimitive.Root>) {
  return (
    <RadioGroupPrimitive.Root
      data-slot="radio-group"
      className={cn("grid gap-3", className)}
      {...props}
    />
  );
}

/** The ring and dot of a radio; it shows checked inside an item with `group/radio`. */
function RadioDot() {
  return (
    <span
      aria-hidden
      className="border-muted-foreground/70 group-data-[state=checked]/radio:border-foreground grid size-[18px] shrink-0 place-items-center rounded-full border-[1.5px]"
    >
      <RadioGroupPrimitive.Indicator className="bg-foreground size-2 rounded-full" />
    </span>
  );
}

function RadioGroupItem({
  className,
  ...props
}: React.ComponentProps<typeof RadioGroupPrimitive.Item>) {
  return (
    <RadioGroupPrimitive.Item
      data-slot="radio-group-item"
      className={cn(
        "group/radio focus-visible:ring-ring/50 rounded-full outline-none focus-visible:ring-[3px] disabled:cursor-not-allowed disabled:opacity-50",
        className,
      )}
      {...props}
    >
      <RadioDot />
    </RadioGroupPrimitive.Item>
  );
}

/** A whole card that is one radio: a dot, a name and an optional line under it. */
function RadioCardItem({
  className,
  label,
  hint,
  ...props
}: Omit<React.ComponentProps<typeof RadioGroupPrimitive.Item>, "children"> & {
  label: string;
  hint?: string;
}) {
  return (
    <RadioGroupPrimitive.Item
      data-slot="radio-card-item"
      className={cn(
        "group/radio border-border hover:bg-accent/60 focus-visible:ring-ring/50 data-[state=checked]:border-foreground/70 data-[state=checked]:bg-accent grid grid-cols-[18px_minmax(0,1fr)] items-center gap-x-2.5 gap-y-0.5 rounded-xl border px-3.5 py-3 text-left outline-none focus-visible:ring-[3px] disabled:cursor-not-allowed disabled:opacity-50",
        className,
      )}
      {...props}
    >
      <RadioDot />
      <span className="truncate text-sm font-semibold">{label}</span>
      {hint ? (
        <span className="text-muted-foreground col-start-2 text-[12.5px]">{hint}</span>
      ) : null}
    </RadioGroupPrimitive.Item>
  );
}

export { RadioGroup, RadioGroupItem, RadioCardItem, RadioDot };
