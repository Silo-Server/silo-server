import { V2ProblemError, type Problem } from "./request";

/**
 * Builds the documented v2 error a test wants a mocked `v2(...)` call to
 * reject with: an RFC 9457 problem of one status, shaped the way the server
 * emits it. Not a test file itself; imported by hook tests.
 */
export function v2Problem(
  status: number,
  type: string,
  detail: string,
  options: { retryAfterSeconds?: number } = {},
): V2ProblemError {
  const problem: Problem = {
    type: `https://siloserver.org/docs/api/v2/problems/${type}`,
    title: type.replace(/_/g, " "),
    status,
    detail,
    instance: "urn:silo:request:test",
  };
  return new V2ProblemError("test", problem, options.retryAfterSeconds ?? null);
}
