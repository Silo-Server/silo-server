/**
 * The player module's request boundary for Silo's native `/api/v2` contract.
 *
 * Mirrors `playerFetch` for the v2 surface: the operation is chosen by its
 * `METHOD /path` literal and the path parameters, JSON body and 2xx response
 * are inferred from the generated contract (a type-only import, erased at
 * build time, so the module still ships without app code). The URL, bearer
 * token, profile and device headers all come from PlayerConfig — the host
 * decides where the server is, the player never assumes the app's session.
 *
 * Documented non-2xx responses are RFC 9457 Problem Details; they are thrown
 * as PlayerFetchError with the problem's detail as the message and its type
 * identifier as the code, so callers handle v1 and v2 failures the same way.
 */

import type { paths } from "@/api/v2/schema";
import type { PlayerConfig } from "./context/PlayerConfigContext";
import { PlayerFetchError, playerRequestHeaders } from "./player-fetch";

type SplitKey<K extends string> = K extends `${infer M} ${infer P}` ? [Lowercase<M>, P] : never;

type OperationOf<K extends string> =
  SplitKey<K> extends [infer M extends string, infer P extends keyof paths]
    ? M extends keyof paths[P]
      ? NonNullable<paths[P][M]>
      : never
    : never;

type PathParamsOf<Op> = Op extends { parameters: { path: infer P extends object } } ? P : never;

type QueryOf<Op> = Op extends { parameters: { query?: infer Q extends object } } ? Q : never;

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

/** Every `METHOD /api/v2/...` literal the generated contract can type. */
export type PlayerV2Key = `${string} ${keyof paths & string}`;

/** The JSON request body of a v2 operation (`never` when it has none). */
export type PlayerV2Body<K extends PlayerV2Key> = BodyOf<OperationOf<K>>;

export type PlayerV2Options<K extends PlayerV2Key> = {
  signal?: AbortSignal;
  query?: QueryOf<OperationOf<K>>;
} & ([PathParamsOf<OperationOf<K>>] extends [never]
  ? unknown
  : { path: PathParamsOf<OperationOf<K>> }) &
  ([PlayerV2Body<K>] extends [never] ? unknown : { body: PlayerV2Body<K> });

interface ProblemLike {
  type?: string;
  title?: string;
  detail?: string;
}

/**
 * The origin the v2 routes hang off. PlayerConfig names the v1 base
 * ("/api/v1" or "https://host/api/v1"); the v2 route literals already carry
 * their "/api/v2/..." prefix, so only the part before "/api/v1" is kept.
 */
export function playerV2Origin(config: PlayerConfig): string {
  return config.apiBaseUrl.replace(/\/api\/v1\/?$/, "");
}

function buildV2Url(route: string, pathParams: Record<string, string | number> | undefined) {
  return route.replace(/\{([^}]+)\}/g, (_match, name: string) => {
    const value = pathParams?.[name];
    if (value === undefined) {
      throw new Error(`playerV2: missing path parameter "${name}" for ${route}`);
    }
    return encodeURIComponent(String(value));
  });
}

/**
 * Performs one v2 request with the player's own credentials. Resolves to the
 * decoded 2xx body (undefined for an empty response) and throws
 * PlayerFetchError for anything else.
 */
export async function playerV2<K extends PlayerV2Key>(
  config: PlayerConfig,
  key: K,
  options: PlayerV2Options<K>,
): Promise<SuccessOf<OperationOf<K>>> {
  const [method, route] = key.split(" ", 2) as [string, string];
  const { signal, path, query, body } = options as {
    signal?: AbortSignal;
    path?: Record<string, string | number>;
    query?: Record<string, unknown>;
    body?: unknown;
  };

  const headers = playerRequestHeaders(config, { Accept: "application/json" }, body !== undefined);
  const params = new URLSearchParams();
  for (const [name, value] of Object.entries(query ?? {})) {
    for (const entry of Array.isArray(value) ? value : [value]) {
      if (entry !== undefined && entry !== null) params.append(name, String(entry));
    }
  }
  const search = params.size > 0 ? `?${params}` : "";
  const res = await fetch(`${playerV2Origin(config)}${buildV2Url(route, path)}${search}`, {
    method,
    headers,
    signal,
    body: body === undefined ? undefined : JSON.stringify(body),
  });

  const text = await res.text().catch(() => "");
  if (!res.ok) {
    let message = res.statusText || "Request failed";
    let code: string | undefined;
    try {
      const problem = JSON.parse(text) as ProblemLike;
      const detail = problem.detail?.trim() || problem.title?.trim();
      if (detail) message = detail;
      if (typeof problem.type === "string") {
        const segment = problem.type.split("?")[0]?.split("/").pop() ?? "";
        code = segment.replace(/#.*$/, "") || undefined;
      }
    } catch {
      if (text.trim().length > 0) message = text.trim();
    }
    throw new PlayerFetchError(res.status, message, code, text);
  }

  if (text.trim() === "") return undefined as SuccessOf<OperationOf<K>>;
  return JSON.parse(text) as SuccessOf<OperationOf<K>>;
}
