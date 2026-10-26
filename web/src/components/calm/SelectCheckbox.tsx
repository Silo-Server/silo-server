import { useRef, type Ref } from "react";
import { Checkbox } from "@/components/ui/checkbox";
import { cn } from "@/lib/utils";

/**
 * A select-mode checkbox. Shift-click and Shift+Space both ask for a range,
 * so a keyboard user can pick rows from the last one they picked.
 */
export function SelectCheckbox({
  label,
  checked,
  onChange,
  ref,
  disabled,
  className,
}: {
  label: string;
  checked: boolean | "indeterminate";
  onChange: (checked: boolean, extendRange: boolean) => void;
  ref?: Ref<HTMLButtonElement>;
  disabled?: boolean;
  className?: string;
}) {
  // A Space press activates the checkbox on key up with a click that may not
  // carry the Shift state, so it is read from the key down.
  const shiftSpace = useRef(false);
  return (
    <Checkbox
      ref={ref}
      aria-label={label}
      checked={checked}
      disabled={disabled}
      className={cn("border-muted-foreground/70 size-[18px] rounded-[5px]", className)}
      onKeyDown={(event) => {
        if (event.key === " ") shiftSpace.current = event.shiftKey;
      }}
      onClick={(event) => {
        // The click decides; Radix's own toggle only reports back through
        // onCheckedChange, which is not used.
        event.preventDefault();
        const extendRange = event.shiftKey || shiftSpace.current;
        shiftSpace.current = false;
        onChange(checked !== true, extendRange);
      }}
    />
  );
}
