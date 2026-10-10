import { useId } from "react";
import { Switch } from "@/components/ui/switch";

/** Settings > Home Screen, under the list: the profile's Hide watched items setting. */
export function HideWatchedCard({
  checked,
  disabled,
  onCheckedChange,
}: {
  checked: boolean;
  disabled: boolean;
  onCheckedChange: (checked: boolean) => void;
}) {
  const id = useId();
  return (
    <div className="surface-panel flex items-center justify-between gap-4 rounded-[22px] px-[22px] py-5">
      <div className="min-w-0 space-y-1">
        <label htmlFor={id} className="text-sm font-semibold">
          Hide watched items
        </label>
        <p id={`${id}-help`} className="text-muted-foreground text-[13px] leading-relaxed">
          Remove titles you&apos;ve finished from ordinary rows. Hero banners and watch-history rows
          keep them.
        </p>
      </div>
      <Switch
        id={id}
        aria-describedby={`${id}-help`}
        checked={checked}
        disabled={disabled}
        onCheckedChange={(next) => onCheckedChange(next === true)}
      />
    </div>
  );
}
