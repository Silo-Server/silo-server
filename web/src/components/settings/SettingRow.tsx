import { useId, type ReactNode } from "react";

import { Label } from "@/components/ui/label";

interface SettingRowProps {
  label: string;
  description: string;
  control: (id: string) => ReactNode;
}

export function SettingRow({ label, description, control }: SettingRowProps) {
  const id = useId();

  return (
    <div className="border-border/50 grid gap-3 border-t pt-4 first:border-t-0 first:pt-0 md:grid-cols-[minmax(0,1fr)_auto] md:items-center">
      <div className="min-w-0 space-y-1">
        <Label htmlFor={id} className="text-sm font-medium">
          {label}
        </Label>
        <p className="text-muted-foreground text-[13px] leading-relaxed">{description}</p>
      </div>
      <div className="flex md:justify-end">{control(id)}</div>
    </div>
  );
}
