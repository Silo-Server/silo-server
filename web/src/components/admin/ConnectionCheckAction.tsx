import type { ConnectionCheckResponse } from "@/api/types";
import { Button } from "@/components/ui/button";

type Props = {
  onClick: () => void | Promise<void>;
  result: ConnectionCheckResponse | null;
  isPending?: boolean;
  disabled?: boolean;
  label?: string;
  pendingLabel?: string;
};

export function ConnectionCheckAction({
  onClick,
  result,
  isPending = false,
  disabled = false,
  label = "Check Connection",
  pendingLabel = "Checking...",
}: Props) {
  return (
    // py matches SettingFieldRow so the action reads as one more row in the
    // group instead of hugging the hairline under the last field.
    <div className="flex flex-wrap items-center gap-3 py-3.5">
      <Button
        type="button"
        size="sm"
        // Filled rather than outline: inside a group's inset panel a
        // transparent hairline button reads as flat text. `secondary` keeps the
        // check subordinate to the primary Save action in the save bar.
        variant="secondary"
        onClick={() => void onClick()}
        disabled={disabled || isPending}
      >
        {isPending ? pendingLabel : label}
      </Button>
      {result ? (
        <span
          role="status"
          aria-live="polite"
          className={`text-sm ${result.success ? "text-green-600" : "text-red-600"}`}
        >
          {result.message}
        </span>
      ) : null}
    </div>
  );
}
