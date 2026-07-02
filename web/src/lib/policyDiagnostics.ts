import type { Diagnostic } from "@codemirror/lint";
import { Text } from "@codemirror/state";

import type { PolicyCompileIssue } from "@/api/types";

function clamp(value: number, min: number, max: number) {
  return Math.min(Math.max(value, min), max);
}

export function mapPolicyIssuesToDiagnostics(
  source: string,
  issues: readonly PolicyCompileIssue[],
): Diagnostic[] {
  const doc = Text.of(source.split(/\r\n|\r|\n/));
  const docLength = doc.length;

  return issues.map((issue) => {
    const row = Number.isFinite(issue.row) && issue.row > 0 ? issue.row : 1;
    const col = Number.isFinite(issue.col) && issue.col > 0 ? issue.col : 1;
    const line = doc.line(clamp(row, 1, doc.lines));
    const from = clamp(line.from + col - 1, line.from, line.to);
    const to = clamp(Math.max(from + 1, line.to === line.from ? from : from + 1), from, docLength);

    return {
      from,
      to,
      severity: "error",
      message: issue.message || "Policy compile error",
    };
  });
}
