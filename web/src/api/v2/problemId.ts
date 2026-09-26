/**
 * The machine-readable identifier of a v2 problem: the final path segment of
 * its `type` URI. Kept apart from `request.ts` so the session layer in
 * `client.ts`, which `request.ts` builds on, can read it without a cycle.
 */
export function problemId(problem: { type: string }): string {
  const path = problem.type.split("?")[0] ?? "";
  const segment = path.slice(path.lastIndexOf("/") + 1);
  return segment.replace(/#.*$/, "");
}
