import type { ExecutionResult } from "@/api/types";
import { Badge } from "@/components/ui/badge";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";

interface TaskStatusBadgeProps {
  result: Pick<ExecutionResult, "status" | "error_message">;
  className?: string;
}

export function TaskStatusBadge({ result, className }: TaskStatusBadgeProps) {
  const variant =
    result.status === "failed"
      ? "destructive"
      : result.status === "cancelled"
        ? "outline"
        : "secondary";
  const badge = (
    <Badge variant={variant} className={cn("shrink-0", className)}>
      {result.status}
    </Badge>
  );
  const errorMessage = result.error_message?.trim();

  if (result.status !== "failed" || !errorMessage) {
    return badge;
  }

  return (
    <TooltipProvider delayDuration={150}>
      <Tooltip>
        <TooltipTrigger asChild>
          <span className="inline-flex cursor-help" tabIndex={0}>
            {badge}
          </span>
        </TooltipTrigger>
        <TooltipContent side="top" className="max-w-[min(28rem,80vw)] whitespace-pre-wrap">
          {errorMessage}
        </TooltipContent>
      </Tooltip>
    </TooltipProvider>
  );
}
