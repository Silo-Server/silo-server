import { RotateCcw } from "lucide-react";

import { Button } from "@/components/ui/button";

/**
 * The small Reset beside a section title. Pages show it only when there is
 * something to reset; the label says what goes back to which default.
 */
export function ResetButton({
  label,
  disabled,
  onClick,
}: {
  label: string;
  disabled: boolean;
  onClick: () => void;
}) {
  return (
    <Button
      type="button"
      variant="ghost"
      size="sm"
      disabled={disabled}
      aria-label={label}
      onClick={onClick}
    >
      <RotateCcw className="h-3.5 w-3.5" aria-hidden="true" />
      Reset
    </Button>
  );
}
