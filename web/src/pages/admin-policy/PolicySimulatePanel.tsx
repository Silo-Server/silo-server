import { Play } from "lucide-react";
import { useEffect, useMemo, useState } from "react";

import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useSimulatePolicy } from "@/hooks/queries/admin/policy";

import { exampleInputForDomain } from "./policyExamples";
import {
  compileIssuesFromError,
  formatPolicyDomain,
  formatPolicyEvalMicros,
  messageFromError,
  prettyPolicyJson,
} from "./policyPageUtils";

interface PolicySimulatePanelProps {
  domains: readonly string[];
  domain?: string;
  source?: string;
}

export function PolicySimulatePanel({ domains, domain, source }: PolicySimulatePanelProps) {
  const fallbackDomain = domain || domains[0] || "scope";
  const [selectedDomain, setSelectedDomain] = useState(fallbackDomain);
  const [input, setInput] = useState(() => exampleInputForDomain(fallbackDomain));
  const [error, setError] = useState("");
  const [issues, setIssues] = useState(compileIssuesFromError(null));
  const simulate = useSimulatePolicy();

  useEffect(() => {
    setSelectedDomain(fallbackDomain);
    setInput(exampleInputForDomain(fallbackDomain));
  }, [fallbackDomain]);

  const resultJson = useMemo(
    () => prettyPolicyJson(simulate.data?.decision),
    [simulate.data?.decision],
  );

  async function runSimulation() {
    setError("");
    setIssues([]);

    let parsedInput: unknown;
    try {
      parsedInput = JSON.parse(input);
    } catch {
      setError("Simulation input must be valid JSON.");
      return;
    }

    try {
      await simulate.mutateAsync({
        domain: selectedDomain,
        source: source?.trim() ? source : undefined,
        input: parsedInput,
      });
    } catch (err) {
      const nextIssues = compileIssuesFromError(err);
      setIssues(nextIssues);
      setError(
        nextIssues.length > 0
          ? "Policy did not compile for simulation."
          : messageFromError(err, "Simulation failed."),
      );
    }
  }

  return (
    <div className="surface-panel-subtle space-y-4 rounded-2xl p-4">
      <div className="flex flex-col justify-between gap-3 sm:flex-row sm:items-start">
        <div>
          <h3 className="text-sm font-semibold">Simulate</h3>
          <p className="text-muted-foreground mt-1 text-xs">
            Run this draft against a sample policy input before activation.
          </p>
        </div>
        <Button type="button" size="sm" onClick={runSimulation} disabled={simulate.isPending}>
          <Play className="size-4" />
          {simulate.isPending ? "Running..." : "Run"}
        </Button>
      </div>

      <div className="grid gap-4 lg:grid-cols-[180px_1fr]">
        <div className="space-y-2">
          <Label htmlFor="policy-sim-domain">Domain</Label>
          <Select
            value={selectedDomain}
            onValueChange={(value) => {
              setSelectedDomain(value);
              setInput(exampleInputForDomain(value));
            }}
          >
            <SelectTrigger id="policy-sim-domain" className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {(domains.length ? domains : [selectedDomain]).map((item) => (
                <SelectItem key={item} value={item}>
                  {formatPolicyDomain(item)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>

        <div className="space-y-2">
          <Label htmlFor="policy-sim-input">Input JSON</Label>
          <textarea
            id="policy-sim-input"
            value={input}
            onChange={(event) => setInput(event.target.value)}
            className="border-border bg-background focus-visible:ring-ring/60 min-h-[220px] w-full rounded-lg border px-3 py-2 font-mono text-xs outline-none focus-visible:ring-2"
            spellCheck={false}
          />
        </div>
      </div>

      {error && (
        <div className="border-destructive/40 bg-destructive/10 text-destructive rounded-lg border px-3 py-2 text-sm">
          {error}
        </div>
      )}

      {issues.length > 0 && (
        <ul className="text-destructive space-y-1 text-xs">
          {issues.map((issue, index) => (
            <li key={`${issue.row}-${issue.col}-${index}`}>
              {issue.row > 0 ? `${issue.row}:${issue.col} ` : ""}
              {issue.message}
            </li>
          ))}
        </ul>
      )}

      {simulate.data && (
        <div className="space-y-2">
          <div className="text-muted-foreground flex flex-wrap gap-3 text-xs">
            <span>Eval: {formatPolicyEvalMicros(simulate.data.eval_time_ns)}</span>
            <span>Generation: {simulate.data.generation}</span>
          </div>
          <pre className="border-border bg-background max-h-[300px] overflow-auto rounded-lg border p-3 font-mono text-xs">
            {resultJson}
          </pre>
        </div>
      )}
    </div>
  );
}
