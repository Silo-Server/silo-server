import { messageFromError } from "./policyPageUtils";
import { ChevronDown, ChevronUp } from "lucide-react";
import { Fragment, useMemo, useState } from "react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
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
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  usePolicyDecision,
  usePolicyDecisions,
  type PolicyDecisionFilters,
} from "@/hooks/queries/admin/policy";
import { useT } from "@/i18n";

import {
  formatPolicyDate,
  formatPolicyDomain,
  formatPolicyEvalMicros,
  prettyPolicyJson,
  toRFC3339FromLocalInput,
} from "./policyPageUtils";

interface PolicyDecisionLogTableProps {
  domains: readonly string[];
}

function decisionNameForDomain(domain: string) {
  return domain.includes(".") ? domain : `silo.${domain}.decision`;
}

function decisionLabel(value: string) {
  const match = /^silo\.(.+)\.decision$/.exec(value);
  return match ? formatPolicyDomain(match[1] ?? value) : value;
}

export function PolicyDecisionLogTable({ domains }: PolicyDecisionLogTableProps) {
  const t = useT();
  const [decisionName, setDecisionName] = useState("all");
  const [userID, setUserID] = useState("");
  const [allowed, setAllowed] = useState("all");
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  const [cursor, setCursor] = useState<string | undefined>(undefined);
  const [cursorStack, setCursorStack] = useState<string[]>([]);
  const [selectedID, setSelectedID] = useState<string | undefined>(undefined);
  const [appliedFilters, setAppliedFilters] = useState<PolicyDecisionFilters>({ limit: 25 });

  const filters = useMemo(
    () => ({
      ...appliedFilters,
      cursor,
    }),
    [appliedFilters, cursor],
  );
  const decisions = usePolicyDecisions(filters);
  const selectedDecision = usePolicyDecision(selectedID);

  function applyFilters() {
    const parsedUserID = userID.trim();
    setAppliedFilters({
      limit: 25,
      decision_name: decisionName === "all" ? undefined : decisionName,
      user_id: parsedUserID || undefined,
      allowed: allowed === "all" ? undefined : allowed === "true" ? "true" : "false",
      from: toRFC3339FromLocalInput(from),
      to: toRFC3339FromLocalInput(to),
    });
    setCursor(undefined);
    setCursorStack([]);
    setSelectedID(undefined);
  }

  function resetFilters() {
    setDecisionName("all");
    setUserID("");
    setAllowed("all");
    setFrom("");
    setTo("");
    setAppliedFilters({ limit: 25 });
    setCursor(undefined);
    setCursorStack([]);
    setSelectedID(undefined);
  }

  function goNext() {
    if (!decisions.data?.page?.next_cursor) return;
    setCursorStack((prev) => [...prev, cursor ?? ""]);
    setCursor(decisions.data.page?.next_cursor);
    setSelectedID(undefined);
  }

  function goPrevious() {
    const next = [...cursorStack];
    const previous = next.pop();
    setCursorStack(next);
    setCursor(previous || undefined);
    setSelectedID(undefined);
  }

  return (
    <div className="space-y-4">
      {decisions.error && (
        <div role="alert">
          <p>
            {messageFromError(decisions.error, t("web.admin.access.policy.decisions.loadFailed"))}
          </p>
          <Button onClick={resetFilters}>{t("web.admin.access.policy.decisions.restart")}</Button>
        </div>
      )}
      <div className="surface-panel-subtle grid gap-3 rounded-2xl p-4 md:grid-cols-[minmax(160px,220px)_120px_130px_1fr_1fr_auto] md:items-end">
        <div className="space-y-2">
          <Label htmlFor="policy-decision-name">
            {t("web.admin.access.policy.decisions.filter.decision")}
          </Label>
          <Select value={decisionName} onValueChange={setDecisionName}>
            <SelectTrigger id="policy-decision-name" className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">
                {t("web.admin.access.policy.decisions.filter.allDecisions")}
              </SelectItem>
              {domains.map((domain) => {
                const value = decisionNameForDomain(domain);
                return (
                  <SelectItem key={value} value={value}>
                    {formatPolicyDomain(domain)}
                  </SelectItem>
                );
              })}
            </SelectContent>
          </Select>
        </div>

        <div className="space-y-2">
          <Label htmlFor="policy-user-id">
            {t("web.admin.access.policy.decisions.filter.userId")}
          </Label>
          <Input
            id="policy-user-id"
            value={userID}
            onChange={(event) => setUserID(event.target.value)}
            inputMode="numeric"
            placeholder={t("web.admin.access.policy.decisions.filter.userIdPlaceholder")}
          />
        </div>

        <div className="space-y-2">
          <Label htmlFor="policy-allowed">
            {t("web.admin.access.policy.decisions.filter.allowed")}
          </Label>
          <Select value={allowed} onValueChange={setAllowed}>
            <SelectTrigger id="policy-allowed" className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">
                {t("web.admin.access.policy.decisions.filter.all")}
              </SelectItem>
              <SelectItem value="true">{t("web.admin.access.policy.outcome.allowed")}</SelectItem>
              <SelectItem value="false">{t("web.admin.access.policy.outcome.denied")}</SelectItem>
            </SelectContent>
          </Select>
        </div>

        <div className="space-y-2">
          <Label htmlFor="policy-from">{t("web.admin.access.policy.decisions.filter.from")}</Label>
          <Input
            id="policy-from"
            type="datetime-local"
            value={from}
            onChange={(event) => setFrom(event.target.value)}
          />
        </div>

        <div className="space-y-2">
          <Label htmlFor="policy-to">{t("web.admin.access.policy.decisions.filter.to")}</Label>
          <Input
            id="policy-to"
            type="datetime-local"
            value={to}
            onChange={(event) => setTo(event.target.value)}
          />
        </div>

        <div className="flex gap-2">
          <Button type="button" variant="outline" onClick={resetFilters}>
            {t("web.admin.access.policy.decisions.filter.reset")}
          </Button>
          <Button type="button" onClick={applyFilters}>
            {t("web.admin.access.policy.decisions.filter.apply")}
          </Button>
        </div>
      </div>

      <div className="surface-panel overflow-hidden rounded-2xl border-0">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t("web.admin.access.policy.decisions.column.time")}</TableHead>
              <TableHead>{t("web.admin.access.policy.decisions.column.decision")}</TableHead>
              <TableHead>{t("web.admin.access.policy.decisions.column.user")}</TableHead>
              <TableHead>{t("web.admin.access.policy.decisions.column.allowed")}</TableHead>
              <TableHead>{t("web.admin.access.policy.decisions.column.eval")}</TableHead>
              <TableHead>{t("web.admin.access.policy.decisions.column.digest")}</TableHead>
              <TableHead className="w-[48px]"> </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {decisions.isLoading && (
              <TableRow>
                <TableCell colSpan={7} className="text-muted-foreground py-6 text-center">
                  {t("web.admin.access.policy.decisions.loading")}
                </TableCell>
              </TableRow>
            )}
            {!decisions.isLoading && decisions.data?.items.length === 0 && (
              <TableRow>
                <TableCell colSpan={7} className="text-muted-foreground py-6 text-center">
                  {t("web.admin.access.policy.decisions.empty")}
                </TableCell>
              </TableRow>
            )}
            {decisions.data?.items.map((entry) => {
              const expanded = selectedID === entry.id;
              return (
                <Fragment key={entry.id}>
                  <TableRow
                    className="cursor-pointer"
                    data-state={expanded ? "selected" : undefined}
                    onClick={() => setSelectedID(expanded ? undefined : entry.id)}
                  >
                    <TableCell>{formatPolicyDate(entry.timestamp)}</TableCell>
                    <TableCell>{decisionLabel(entry.decision_name)}</TableCell>
                    <TableCell>{entry.user_id ?? t("web.admin.access.policy.noValue")}</TableCell>
                    <TableCell>
                      <Badge
                        variant={
                          entry.allowed === false
                            ? "destructive"
                            : entry.allowed === true
                              ? "secondary"
                              : "outline"
                        }
                      >
                        {entry.allowed === false
                          ? t("web.admin.access.policy.outcome.denied")
                          : entry.allowed === true
                            ? t("web.admin.access.policy.outcome.allowed")
                            : t("web.admin.access.policy.outcome.error")}
                      </Badge>
                    </TableCell>
                    <TableCell>{formatPolicyEvalMicros(entry.eval_time_ns)}</TableCell>
                    <TableCell className="max-w-[260px] truncate font-mono text-xs">
                      {entry.input_digest || t("web.admin.access.policy.noValue")}
                    </TableCell>
                    <TableCell className="text-right">
                      {expanded ? (
                        <ChevronUp className="text-muted-foreground ml-auto size-4" />
                      ) : (
                        <ChevronDown className="text-muted-foreground ml-auto size-4" />
                      )}
                    </TableCell>
                  </TableRow>
                  {expanded && (
                    <TableRow key={`${entry.id}-detail`}>
                      <TableCell colSpan={7} className="bg-muted/20 p-4">
                        <DecisionDetail
                          error={entry.error}
                          isLoading={selectedDecision.isLoading}
                          inputDigest={entry.input_digest}
                          inputSample={selectedDecision.data?.input_sample}
                          resultSample={selectedDecision.data?.result_sample}
                        />
                      </TableCell>
                    </TableRow>
                  )}
                </Fragment>
              );
            })}
          </TableBody>
        </Table>
      </div>

      <div className="flex items-center justify-between gap-3">
        <Button
          type="button"
          variant="outline"
          disabled={cursorStack.length === 0 || decisions.isFetching}
          onClick={goPrevious}
        >
          {t("web.admin.access.policy.decisions.previous")}
        </Button>
        <div className="text-muted-foreground text-xs">
          {decisions.isFetching
            ? t("web.admin.access.policy.decisions.refreshing")
            : decisions.data?.page?.next_cursor
              ? t("web.admin.access.policy.decisions.moreRows")
              : ""}
        </div>
        <Button
          type="button"
          variant="outline"
          disabled={!decisions.data?.page?.next_cursor || decisions.isFetching}
          onClick={goNext}
        >
          {t("web.admin.access.policy.decisions.next")}
        </Button>
      </div>
    </div>
  );
}

function DecisionDetail({
  error,
  isLoading,
  inputDigest,
  inputSample,
  resultSample,
}: {
  error?: string;
  isLoading: boolean;
  inputDigest: string;
  inputSample?: unknown;
  resultSample?: unknown;
}) {
  const t = useT();
  if (isLoading) {
    return (
      <p className="text-muted-foreground text-sm">
        {t("web.admin.access.policy.decisions.detail.loading")}
      </p>
    );
  }

  return (
    <div className="grid gap-4 lg:grid-cols-2">
      <div className="space-y-2">
        <h3 className="text-sm font-semibold">
          {t("web.admin.access.policy.decisions.detail.input")}
        </h3>
        <p className="text-muted-foreground font-mono text-xs">
          {t("web.admin.access.policy.decisions.detail.digest", {
            digest: inputDigest || t("web.admin.access.policy.noValue"),
          })}
        </p>
        {inputSample !== undefined && (
          <pre className="border-border bg-background max-h-[260px] overflow-auto rounded-lg border p-3 font-mono text-xs">
            {prettyPolicyJson(inputSample)}
          </pre>
        )}
      </div>
      <div className="space-y-2">
        <h3 className="text-sm font-semibold">
          {t("web.admin.access.policy.decisions.detail.result")}
        </h3>
        {error && <p className="text-destructive text-sm">{error}</p>}
        {resultSample !== undefined && (
          <pre className="border-border bg-background max-h-[260px] overflow-auto rounded-lg border p-3 font-mono text-xs">
            {prettyPolicyJson(resultSample)}
          </pre>
        )}
        {!error && resultSample === undefined && (
          <p className="text-muted-foreground text-sm">
            {t("web.admin.access.policy.decisions.detail.noResult")}
          </p>
        )}
      </div>
    </div>
  );
}
