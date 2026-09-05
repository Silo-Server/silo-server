/**
 * The typed request boundary for Silo's native `/api/v2` contract.
 *
 * `v2("GET /api/v2/progress", { query: { limit: 20 } })` selects an operation
 * by its `METHOD /path` literal and infers the path parameters, query, JSON
 * body, and 2xx response type from the generated `paths` type in ./schema.ts.
 * Documented non-2xx responses are RFC 9457 Problem Details and are thrown as
 * `V2ProblemError`; anything the contract does not describe (an HTML error
 * page, an unparseable body) is a `V2TransportError`; a failed fetch rejects
 * with the network error itself.
 *
 * Session handling (bearer token, refresh-then-retry on 401, profile and
 * device headers) is the same machinery the v1 client uses, shared through
 * `fetchWithSession`. This module never touches `/api/v1`.
 */
import {
  fetchWithSession,
  isProfileRequestContextCurrent,
  reportProfileUnverified,
  StaleApiRequestContextError,
  type ProfileRequestContextSnapshot,
} from "../client";
import { v2Operations } from "./operations";
import type { components, paths } from "./schema";

// ---------------------------------------------------------------------------
// Operation selection: `METHOD /path` literal -> generated operation type.
// The generated `operations` interface carries no method or path, so the
// path-keyed `paths` type is the only one that lets a single string literal
// pin down every part of the request; the operation id is looked up at
// runtime from the generated ./operations.ts map to label errors.
// ---------------------------------------------------------------------------

/** Every `METHOD /path` the committed v2 OpenAPI document declares. */
export type V2OperationKey = keyof typeof v2Operations;

/** The operation id the spec assigns to a `METHOD /path`. */
export type V2OperationId<K extends V2OperationKey> = (typeof v2Operations)[K];

type SplitKey<K extends string> = K extends `${infer M} ${infer P}` ? [Lowercase<M>, P] : never;

type OperationOf<K extends V2OperationKey> =
  SplitKey<K> extends [infer M extends string, infer P extends keyof paths]
    ? M extends keyof paths[P]
      ? NonNullable<paths[P][M]>
      : never
    : never;

type ParamsOf<Op> = Op extends { parameters: infer P } ? P : never;

type PathParamsOf<Op> = ParamsOf<Op> extends { path: infer P extends object } ? P : never;

type QueryOf<Op> =
  ParamsOf<Op> extends { query?: infer Q extends object }
    ? [Q] extends [never]
      ? never
      : Q
    : never;

type BodyOf<Op> = Op extends { requestBody: { content: { "application/json": infer B } } }
  ? B
  : never;

type SuccessStatus = 200 | 201 | 202 | 203 | 204;

type SuccessOf<Op> = Op extends { responses: infer R }
  ? {
      [S in keyof R & SuccessStatus]: R[S] extends { content: { "application/json": infer T } }
        ? T
        : undefined;
    }[keyof R & SuccessStatus]
  : never;

// ---------------------------------------------------------------------------
// Nominal separation from the handwritten v1 mirror (src/api/types.ts).
// A v2 success value carries a compile-time-only brand, so a v1 type that
// happens to share a shape cannot satisfy a parameter typed against the v2
// contract. The brand is applied once, here, after the body is decoded.
// ---------------------------------------------------------------------------

declare const v2Contract: unique symbol;

/** A value decoded from a v2 response. Structural look-alikes from v1 do not carry the brand. */
export type V2<T> = T extends object ? T & { readonly [v2Contract]: "api/v2" } : T;

function brand<T>(value: T): V2<T> {
  // The brand is a phantom type; the decoded object is returned as is.
  return value as V2<T>;
}

/** The 2xx response type of a v2 operation. */
export type V2Result<K extends V2OperationKey> = V2<SuccessOf<OperationOf<K>>>;

/** The JSON request body type of a v2 operation (`never` when it has none). */
export type V2Body<K extends V2OperationKey> = BodyOf<OperationOf<K>>;

/** The query parameter type of a v2 operation (`never` when it has none). */
export type V2Query<K extends V2OperationKey> = QueryOf<OperationOf<K>>;

/** The path parameter type of a v2 operation (`never` when it has none). */
export type V2PathParams<K extends V2OperationKey> = PathParamsOf<OperationOf<K>>;

// ---------------------------------------------------------------------------
// Request options: only the parts the operation declares are accepted, and
// the whole options object is optional when nothing is required.
// ---------------------------------------------------------------------------

type QueryValue = string | number | boolean | null | undefined;

interface CommonOptions {
  signal?: AbortSignal;
  /**
   * A captured account/profile authority for a queued write. The request
   * carries the snapshot's bearer token and profile/PIN headers rather than
   * whatever session is active when it is finally sent, and a stale snapshot
   * (logout, account or server switch) is rejected before and after fetch,
   * exactly as `apiWithProfileRequestContext` does for v1.
   */
  profileContext?: ProfileRequestContextSnapshot;
}

export type V2RequestOptions<K extends V2OperationKey> = CommonOptions &
  ([V2PathParams<K>] extends [never] ? unknown : { path: V2PathParams<K> }) &
  ([V2Query<K>] extends [never] ? unknown : { query?: V2Query<K> }) &
  ([V2Body<K>] extends [never] ? unknown : { body: V2Body<K> });

type RequestArgs<K extends V2OperationKey> =
  Record<never, never> extends V2RequestOptions<K>
    ? [options?: V2RequestOptions<K>]
    : [options: V2RequestOptions<K>];

// ---------------------------------------------------------------------------
// Problem Details.
// ---------------------------------------------------------------------------

/** An RFC 9457 problem as the v2 contract emits it (application/problem+json). */
export type Problem = components["schemas"]["Problem"];

/** One field-level validation detail inside a `validation_failed` problem. */
export type ProblemError = components["schemas"]["ProblemError"];

/** The machine-readable identifier: the final path segment of `Problem.type`. */
export function problemId(problem: Pick<Problem, "type">): string {
  const path = problem.type.split("?")[0] ?? "";
  const segment = path.slice(path.lastIndexOf("/") + 1);
  return segment.replace(/#.*$/, "");
}

/** A documented v2 error: the server answered with a Problem Details body. */
export class V2ProblemError extends Error {
  readonly operationId: string;
  readonly problem: Problem;
  readonly status: number;
  /** The problem identifier, e.g. `authentication_required` or `validation_failed`. */
  readonly problemType: string;

  constructor(operationId: string, problem: Problem) {
    super(problem.detail || problem.title);
    this.name = "V2ProblemError";
    this.operationId = operationId;
    this.problem = problem;
    this.status = problem.status;
    this.problemType = problemId(problem);
  }
}

/**
 * A response the contract does not describe: a non-JSON body (an HTML error
 * page from a proxy, a truncated stream) or a status without a problem
 * document. Distinct from `V2ProblemError` so callers never treat a gateway
 * page as a contract answer.
 */
export class V2TransportError extends Error {
  readonly operationId: string;
  readonly status: number;

  constructor(operationId: string, status: number, reason: string) {
    super(`${operationId}: ${reason} (HTTP ${status})`);
    this.name = "V2TransportError";
    this.operationId = operationId;
    this.status = status;
  }
}

// ---------------------------------------------------------------------------
// The request.
// ---------------------------------------------------------------------------

/** Identifies this client to the server; the same header pair the contract fixtures send. */
export const V2_CLIENT_HEADERS: Readonly<Record<string, string>> = {
  "X-Silo-Client": "Silo Web",
  "X-Silo-Client-Version": typeof __SILO_WEB_VERSION__ === "string" ? __SILO_WEB_VERSION__ : "dev",
};

function isProblem(value: unknown): value is Problem {
  if (typeof value !== "object" || value === null) return false;
  const candidate = value as Record<string, unknown>;
  return (
    typeof candidate.type === "string" &&
    typeof candidate.title === "string" &&
    typeof candidate.status === "number"
  );
}

function isJsonMediaType(contentType: string | null): boolean {
  if (!contentType) return false;
  const mediaType = contentType.split(";")[0]?.trim().toLowerCase() ?? "";
  return mediaType === "application/json" || mediaType.endsWith("+json");
}

function buildUrl(
  route: string,
  pathParams: Record<string, string | number> | undefined,
  query: Record<string, QueryValue> | undefined,
): string {
  const url = route.replace(/\{([^}]+)\}/g, (_match, name: string) => {
    const value = pathParams?.[name];
    if (value === undefined) {
      throw new Error(`v2: missing path parameter "${name}" for ${route}`);
    }
    return encodeURIComponent(String(value));
  });
  if (!query) return url;
  const search = new URLSearchParams();
  for (const [name, value] of Object.entries(query)) {
    if (value === undefined || value === null) continue;
    search.set(name, String(value));
  }
  const encoded = search.toString();
  return encoded ? `${url}?${encoded}` : url;
}

async function readBody(res: Response, operationId: string): Promise<unknown> {
  const text = await res.text();
  if (text.trim() === "") return undefined;
  if (!isJsonMediaType(res.headers.get("Content-Type"))) {
    throw new V2TransportError(operationId, res.status, "the response body is not JSON");
  }
  try {
    return JSON.parse(text) as unknown;
  } catch {
    throw new V2TransportError(operationId, res.status, "the response body is not valid JSON");
  }
}

/**
 * Performs one v2 request. The operation is chosen by its `METHOD /path`
 * literal; the options are inferred from the generated contract.
 */
export async function v2<K extends V2OperationKey>(
  key: K,
  ...args: RequestArgs<K>
): Promise<V2Result<K>> {
  const [method, route] = key.split(" ", 2) as [string, string];
  const options = (args[0] ?? {}) as CommonOptions & {
    path?: Record<string, string | number>;
    query?: Record<string, QueryValue>;
    body?: unknown;
  };

  const headers: Record<string, string> = {
    Accept: "application/json",
    ...V2_CLIENT_HEADERS,
  };
  const init: RequestInit = { method, headers, signal: options.signal };
  if (options.body !== undefined) {
    headers["Content-Type"] = "application/json";
    init.body = JSON.stringify(options.body);
  }
  const snapshot = options.profileContext;
  if (snapshot) {
    // Explicit headers win over the active session inside fetchWithSession.
    headers.Authorization = `Bearer ${snapshot.accessToken}`;
    headers["X-Profile-Id"] = snapshot.profileId;
    headers["X-Profile-Token"] = snapshot.profileToken ?? "";
  }

  const { res, requestProfileId, requestProfileToken } = await fetchWithSession(
    buildUrl(route, options.path, options.query),
    init,
    snapshot,
  );
  if (snapshot && !isProfileRequestContextCurrent(snapshot)) {
    throw new StaleApiRequestContextError();
  }

  try {
    return await decodeV2Response(key, res);
  } catch (err) {
    if (
      err instanceof V2ProblemError &&
      err.status === 403 &&
      err.problemType === "profile_verification_required"
    ) {
      reportProfileUnverified(requestProfileId, requestProfileToken, snapshot);
    }
    throw err;
  }
}

/**
 * Decodes one v2 response for the operation `key`: the branded success body,
 * or a thrown `V2ProblemError` / `V2TransportError`. Exposed for the few
 * callers that must issue the fetch themselves (an explicit bearer token
 * outside the shared session) and still want contract-shaped answers.
 */
export async function decodeV2Response<K extends V2OperationKey>(
  key: K,
  res: Response,
): Promise<V2Result<K>> {
  const operationId: string = v2Operations[key];
  if (res.ok) {
    const decoded = await readBody(res, operationId);
    return brand(decoded as SuccessOf<OperationOf<K>>);
  }

  const body = await readBody(res, operationId);
  if (!isProblem(body)) {
    throw new V2TransportError(
      operationId,
      res.status,
      "the error response is not a problem document",
    );
  }
  throw new V2ProblemError(operationId, body);
}
