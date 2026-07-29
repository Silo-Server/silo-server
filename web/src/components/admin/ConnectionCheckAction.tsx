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
    <div className="flex flex-wrap items-center gap-3 pt-1">
      <Button
        type="button"
        size="sm"
        variant="outline"
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
