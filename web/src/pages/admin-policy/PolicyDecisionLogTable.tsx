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
  const [decisionName, setDecisionName] = useState("all");
  const [userID, setUserID] = useState("");
  const [allowed, setAllowed] = useState("all");
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  const [cursor, setCursor] = useState<string | undefined>(undefined);
  const [cursorStack, setCursorStack] = useState<string[]>([]);
  const [selectedID, setSelectedID] = useState<number | undefined>(undefined);
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
    const parsedUserID = Number.parseInt(userID, 10);
    setAppliedFilters({
      limit: 25,
      decision_name: decisionName === "all" ? undefined : decisionName,
      user_id: Number.isFinite(parsedUserID) ? parsedUserID : undefined,
      allowed: allowed === "all" ? undefined : allowed === "true",
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
    if (!decisions.data?.next_cursor) return;
    setCursorStack((prev) => [...prev, cursor ?? ""]);
    setCursor(decisions.data.next_cursor);
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
      <div className="surface-panel-subtle grid gap-3 rounded-2xl p-4 md:grid-cols-[minmax(160px,220px)_120px_130px_1fr_1fr_auto] md:items-end">
        <div className="space-y-2">
          <Label htmlFor="policy-decision-name">Decision</Label>
          <Select value={decisionName} onValueChange={setDecisionName}>
            <SelectTrigger id="policy-decision-name" className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">All decisions</SelectItem>
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
          <Label htmlFor="policy-user-id">User ID</Label>
          <Input
            id="policy-user-id"
            value={userID}
            onChange={(event) => setUserID(event.target.value)}
            inputMode="numeric"
            placeholder="42"
          />
        </div>

        <div className="space-y-2">
          <Label htmlFor="policy-allowed">Allowed</Label>
          <Select value={allowed} onValueChange={setAllowed}>
            <SelectTrigger id="policy-allowed" className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">All</SelectItem>
              <SelectItem value="true">Allowed</SelectItem>
              <SelectItem value="false">Denied</SelectItem>
            </SelectContent>
          </Select>
        </div>

        <div className="space-y-2">
          <Label htmlFor="policy-from">From</Label>
          <Input
            id="policy-from"
            type="datetime-local"
            value={from}
            onChange={(event) => setFrom(event.target.value)}
          />
        </div>

        <div className="space-y-2">
          <Label htmlFor="policy-to">To</Label>
          <Input
            id="policy-to"
            type="datetime-local"
            value={to}
            onChange={(event) => setTo(event.target.value)}
          />
        </div>

        <div className="flex gap-2">
          <Button type="button" variant="outline" onClick={resetFilters}>
            Reset
          </Button>
          <Button type="button" onClick={applyFilters}>
            Apply
          </Button>
        </div>
      </div>

      <div className="surface-panel overflow-hidden rounded-2xl border-0">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Time</TableHead>
              <TableHead>Decision</TableHead>
              <TableHead>User</TableHead>
              <TableHead>Allowed</TableHead>
              <TableHead>Eval</TableHead>
              <TableHead>Digest</TableHead>
              <TableHead className="w-[48px]"> </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {decisions.isLoading && (
              <TableRow>
                <TableCell colSpan={7} className="text-muted-foreground py-6 text-center">
                  Loading policy decisions...
                </TableCell>
              </TableRow>
            )}
            {!decisions.isLoading && decisions.data?.entries.length === 0 && (
              <TableRow>
                <TableCell colSpan={7} className="text-muted-foreground py-6 text-center">
                  No policy decisions matched the current filters.
                </TableCell>
              </TableRow>
            )}
            {decisions.data?.entries.map((entry) => {
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
                    <TableCell>{entry.user_id ?? "—"}</TableCell>
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
                          ? "Denied"
                          : entry.allowed === true
                            ? "Allowed"
                            : "Error"}
                      </Badge>
                    </TableCell>
                    <TableCell>{formatPolicyEvalMicros(entry.eval_time_ns)}</TableCell>
                    <TableCell className="max-w-[260px] truncate font-mono text-xs">
                      {entry.input_digest || "—"}
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
          Previous
        </Button>
        <div className="text-muted-foreground text-xs">
          {decisions.isFetching ? "Refreshing..." : decisions.data?.next_cursor ? "More rows" : ""}
        </div>
        <Button
          type="button"
          variant="outline"
          disabled={!decisions.data?.next_cursor || decisions.isFetching}
          onClick={goNext}
        >
          Next
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
  if (isLoading) {
    return <p className="text-muted-foreground text-sm">Loading decision details...</p>;
  }

  return (
    <div className="grid gap-4 lg:grid-cols-2">
      <div className="space-y-2">
        <h3 className="text-sm font-semibold">Input</h3>
        <p className="text-muted-foreground font-mono text-xs">Digest: {inputDigest || "—"}</p>
        {inputSample !== undefined && (
          <pre className="border-border bg-background max-h-[260px] overflow-auto rounded-lg border p-3 font-mono text-xs">
            {prettyPolicyJson(inputSample)}
          </pre>
        )}
      </div>
      <div className="space-y-2">
        <h3 className="text-sm font-semibold">Result</h3>
        {error && <p className="text-destructive text-sm">{error}</p>}
        {resultSample !== undefined && (
          <pre className="border-border bg-background max-h-[260px] overflow-auto rounded-lg border p-3 font-mono text-xs">
            {prettyPolicyJson(resultSample)}
          </pre>
        )}
        {!error && resultSample === undefined && (
          <p className="text-muted-foreground text-sm">No verbose result sample was logged.</p>
        )}
      </div>
    </div>
  );
}
