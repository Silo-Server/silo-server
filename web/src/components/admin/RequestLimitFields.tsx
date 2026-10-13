import { useId, useState } from "react";

import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  isZeroRequestLimit,
  type RequestApprovalChoice,
  type RequestLimitChoice,
  type RequestLimitDraft,
  type RequestLimitErrors,
} from "@/lib/requestAccess";
import { cn } from "@/lib/utils";

export interface RequestLimitFieldsProps {
  draft: RequestLimitDraft;
  onChange: (draft: RequestLimitDraft) => void;
  /** Whose limit this is, for the hint a limit of zero shows. */
  subject: "account" | "group";
  /** The inherit option's label: "Use group default" or "Use server default". */
  inheritLabel: string;
  /** What each field resolves to while it inherits, shown under it. */
  inherited?: { approval: string; limit: string };
  /** The numbers a limit starts from when it is switched to custom with none typed. */
  customSeed: { maxRequests: string; windowDays: string };
  errors: RequestLimitErrors;
  disabled?: boolean;
  /** One field under the other, for a narrow panel; side by side from `sm` otherwise. */
  stacked?: boolean;
}

/** Space-separated ids for aria-describedby, or undefined when there are none. */
function describedBy(...ids: Array<string | false>) {
  return ids.filter(Boolean).join(" ") || undefined;
}

/**
 * Request approval and limit for an account or an access group. Blocking is
 * not offered here: that is the requests switch on the account or group.
 */
export function RequestLimitFields({
  draft,
  onChange,
  subject,
  inheritLabel,
  inherited,
  customSeed,
  errors,
  disabled,
  stacked = false,
}: RequestLimitFieldsProps) {
  const approvalId = useId();
  const approvalHintId = useId();
  const limitId = useId();
  const limitHintId = useId();
  const maxId = useId();
  const maxErrorId = useId();
  const zeroHintId = useId();
  const windowId = useId();
  const windowErrorId = useId();
  // Errors are announced once the admin edits the numbers, not when a stored
  // value is loaded into the form.
  const [edited, setEdited] = useState(false);

  const approvalHint = draft.approval === "inherit" && inherited ? inherited.approval : undefined;
  const limitHint = draft.limit === "inherit" && inherited ? inherited.limit : undefined;
  const custom = draft.limit === "custom";
  const zero = isZeroRequestLimit(draft);

  function setLimit(limit: RequestLimitChoice) {
    if (limit === "custom" && draft.maxRequests === "" && draft.windowDays === "") {
      onChange({ ...draft, limit, ...customSeed });
      return;
    }
    onChange({ ...draft, limit });
  }

  function editNumbers(change: Partial<RequestLimitDraft>) {
    setEdited(true);
    onChange({ ...draft, ...change });
  }

  return (
    <div className={cn("grid gap-4", !stacked && "sm:grid-cols-2")}>
      <div className="min-w-0 space-y-2">
        <Label htmlFor={approvalId}>Approval</Label>
        <Select
          value={draft.approval}
          onValueChange={(approval) =>
            onChange({ ...draft, approval: approval as RequestApprovalChoice })
          }
          disabled={disabled}
        >
          <SelectTrigger
            id={approvalId}
            className="w-full"
            aria-describedby={describedBy(approvalHint !== undefined && approvalHintId)}
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="inherit">{inheritLabel}</SelectItem>
            <SelectItem value="manual">An admin approves</SelectItem>
            <SelectItem value="auto">Approve automatically</SelectItem>
          </SelectContent>
        </Select>
        {approvalHint !== undefined ? (
          <p id={approvalHintId} className="text-muted-foreground text-xs">
            {approvalHint}
          </p>
        ) : null}
      </div>

      <div className="min-w-0 space-y-2">
        <Label htmlFor={limitId}>Limit</Label>
        <Select
          value={draft.limit}
          onValueChange={(limit) => setLimit(limit as RequestLimitChoice)}
          disabled={disabled}
        >
          <SelectTrigger
            id={limitId}
            className="w-full"
            aria-describedby={describedBy(limitHint !== undefined && limitHintId)}
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="inherit">{inheritLabel}</SelectItem>
            <SelectItem value="custom">Custom limit</SelectItem>
            <SelectItem value="unlimited">No limit</SelectItem>
          </SelectContent>
        </Select>
        {custom ? (
          <div className="flex flex-wrap items-center gap-2 text-sm">
            <Label htmlFor={maxId} className="sr-only">
              Requests allowed
            </Label>
            <Input
              id={maxId}
              type="number"
              inputMode="numeric"
              min={0}
              step={1}
              className="w-20"
              value={draft.maxRequests}
              onChange={(event) => editNumbers({ maxRequests: event.target.value })}
              aria-invalid={errors.maxRequests ? true : undefined}
              aria-describedby={describedBy(
                Boolean(errors.maxRequests) && maxErrorId,
                zero && zeroHintId,
              )}
              disabled={disabled}
            />
            <span className="text-muted-foreground">requests per</span>
            <Label htmlFor={windowId} className="sr-only">
              Days in the limit window
            </Label>
            <Input
              id={windowId}
              type="number"
              inputMode="numeric"
              min={1}
              step={1}
              className="w-20"
              value={draft.windowDays}
              onChange={(event) => editNumbers({ windowDays: event.target.value })}
              aria-invalid={errors.windowDays ? true : undefined}
              aria-describedby={describedBy(Boolean(errors.windowDays) && windowErrorId)}
              disabled={disabled}
            />
            <span className="text-muted-foreground">days</span>
          </div>
        ) : null}
        {limitHint !== undefined ? (
          <p id={limitHintId} className="text-muted-foreground text-xs">
            {limitHint}
          </p>
        ) : null}
        {zero ? (
          <p id={zeroHintId} className="text-muted-foreground text-xs">
            0 stops new requests; to block this {subject}, turn off Media requests instead.
          </p>
        ) : null}
        {custom && errors.maxRequests ? (
          <p
            id={maxErrorId}
            role={edited ? "alert" : undefined}
            className="text-destructive text-xs"
          >
            {errors.maxRequests}
          </p>
        ) : null}
        {custom && errors.windowDays ? (
          <p
            id={windowErrorId}
            role={edited ? "alert" : undefined}
            className="text-destructive text-xs"
          >
            {errors.windowDays}
          </p>
        ) : null}
      </div>
    </div>
  );
}
