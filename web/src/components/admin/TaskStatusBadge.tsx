import type { ExecutionResult } from "@/api/types";
import { Badge } from "@/components/ui/badge";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { useT, type MessageKey } from "@/i18n";
import { cn } from "@/lib/utils";

interface TaskStatusBadgeProps {
  result: Pick<ExecutionResult, "status" | "error_message" | "task_key" | "result_data">;
  className?: string;
}

const STATUS_LABELS: Record<ExecutionResult["status"], MessageKey> = {
  completed: "web.admin.common.taskStatus.completed",
  failed: "web.admin.common.taskStatus.failed",
  cancelled: "web.admin.common.taskStatus.cancelled",
};

export function TaskStatusBadge({ result, className }: TaskStatusBadgeProps) {
  const t = useT();
  const failed = result.result_data?.failed;
  const completedWithErrors =
    result.status === "completed" &&
    result.task_key === "contribute_markers" &&
    typeof failed === "number" &&
    Number.isFinite(failed) &&
    failed > 0;
  const variant =
    result.status === "failed" || completedWithErrors
      ? "destructive"
      : result.status === "cancelled"
        ? "outline"
        : "secondary";
  const statusKey = STATUS_LABELS[result.status] as MessageKey | undefined;
  // A status this build does not know yet shows as the server sent it.
  const statusLabel = statusKey ? t(statusKey) : result.status;
  const badge = (
    <Badge variant={variant} className={cn("shrink-0", className)}>
      {completedWithErrors ? t("web.admin.common.taskStatus.completedWithErrors") : statusLabel}
    </Badge>
  );
  const errorMessage = result.error_message?.trim();

  if (!errorMessage) {
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
