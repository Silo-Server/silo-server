/**
 * A recording stand-in for the `/api/v2` request boundary, for tests that pin
 * the exact requests a page sends.
 *
 * Install it in a test file with the mock line and the hooks:
 *
 *   vi.mock("@/api/v2/request", async () => (await import("@/test/v2Recorder")).mockV2Request());
 *   installV2Recorder();
 *
 * Every call is recorded as its operation, concrete path, query, headers and
 * the JSON body as it would cross the wire (undefined members dropped). Calls
 * are answered from `contracts/api/v2/fixtures`, or from `v2Recorder.answer()`
 * when a test needs a different answer; an operation with neither fails the
 * test. Each concrete path carries a revision: reads and successful writes
 * answer with its ETag (the path and revision, so no two paths share one), a write bumps it and the collection it belongs to,
 * and a guarded write whose If-Match is not current is answered 412 with the
 * current ETag, as the server does.
 *
 * After each test the recorder checks the wire invariants every collection
 * request must keep (see `wireInvariantViolations`).
 *
 * This module must not import `@/api/v2/request` at runtime: the mock factory
 * loads it, and the real module is reached through `vi.importActual`.
 */
import { cleanup } from "@testing-library/react";
import { afterEach, beforeEach, expect, vi } from "vitest";

import type { V2ProblemError as V2ProblemErrorClass } from "@/api/v2/request";
import adminCollectionStale from "../../../contracts/api/v2/fixtures/admin_collection_stale.json";
import adminTemplateJobAccepted from "../../../contracts/api/v2/fixtures/admin_collection_template_job_accepted.json";
import createCollectionOk from "../../../contracts/api/v2/fixtures/create_collection_ok.json";
import getAdminCollectionOk from "../../../contracts/api/v2/fixtures/get_admin_collection_ok.json";
import getAdminTemplateJobCompleted from "../../../contracts/api/v2/fixtures/get_admin_collection_template_job_completed.json";
import getCatalogFiltersOk from "../../../contracts/api/v2/fixtures/get_catalog_filters_ok.json";
import getCatalogSearchCapabilitiesOk from "../../../contracts/api/v2/fixtures/get_catalog_search_capabilities_ok.json";
import getCollectionItemsOk from "../../../contracts/api/v2/fixtures/get_collection_items_ok.json";
import getCollectionOk from "../../../contracts/api/v2/fixtures/get_collection_ok.json";
import getLibraryCollectionsOk from "../../../contracts/api/v2/fixtures/get_library_collections_ok.json";
import getOverlayConfigOk from "../../../contracts/api/v2/fixtures/get_overlay_config_ok.json";
import getRatingsCapabilityOk from "../../../contracts/api/v2/fixtures/get_ratings_capability_ok.json";
import getSettingsContractCapabilitiesOk from "../../../contracts/api/v2/fixtures/get_settings_contract_capabilities_ok.json";
import listCollectionsOk from "../../../contracts/api/v2/fixtures/list_collections_ok.json";
import queryCatalogItemsOk from "../../../contracts/api/v2/fixtures/query_catalog_items_ok.json";

export interface RecordedCall {
  /** The `METHOD /api/v2/...` operation key, with its path template. */
  operation: string;
  /** The concrete path, path parameters filled in. */
  path: string;
  query?: Record<string, unknown>;
  headers: Record<string, string>;
  /** The JSON body as sent: members whose value is undefined are absent. */
  body?: unknown;
  /** A multipart form: text parts as strings, files as `{ file: name }`. */
  form?: Record<string, string | { file: string }>;
}

type AnswerFn = (call: RecordedCall) => unknown;
type Answer = AnswerFn | { value: unknown };

interface RequestOptions {
  path?: Record<string, string | number>;
  query?: Record<string, unknown>;
  headers?: Record<string, string>;
  body?: unknown;
  form?: Record<string, Blob | string>;
  onResponse?: (response: Response) => void;
}

// Writes the contract guards with a required If-Match.
const GUARDED_OPERATIONS = new Set([
  "PATCH /api/v2/admin/collections/{id}",
  "DELETE /api/v2/admin/collections/{id}",
  "PUT /api/v2/admin/collections/order",
  "PUT /api/v2/admin/collections/{id}/items/order",
  "PATCH /api/v2/admin/collection-groups/{id}",
  "DELETE /api/v2/admin/collection-groups/{id}",
  "PUT /api/v2/admin/collection-groups/{group_id}/collections/order",
  "PUT /api/v2/admin/libraries/{library_id}/collection-groups/order",
  "PATCH /api/v2/collections/{id}",
  "DELETE /api/v2/collections/{id}",
  "PUT /api/v2/collections/order",
  "PUT /api/v2/collections/{id}/items/order",
  "PATCH /api/v2/collections/groups/{id}",
  "DELETE /api/v2/collections/groups/{id}",
  "PUT /api/v2/collections/groups/order",
]);

// POSTs that only read; they neither count as writes nor change a revision.
const READ_ONLY_POSTS = new Set([
  "POST /api/v2/catalog/query",
  "POST /api/v2/collections/preview",
  "POST /api/v2/admin/collections/preview",
]);

const adminImport = { collection: getAdminCollectionOk };
const personalImport = { collection: getCollectionOk };

// Each answer is cloned before it is returned, so a test cannot change a fixture.
const FIXTURE_ANSWERS: Record<string, unknown> = {
  "GET /api/v2/admin/collections/{id}": getAdminCollectionOk,
  "POST /api/v2/admin/collections": getAdminCollectionOk,
  "PATCH /api/v2/admin/collections/{id}": getAdminCollectionOk,
  "PUT /api/v2/admin/collections/{id}/poster": getAdminCollectionOk,
  "PUT /api/v2/admin/collections/{id}/backdrop": getAdminCollectionOk,
  "DELETE /api/v2/admin/collections/{id}/image": undefined,
  "PUT /api/v2/admin/collections/{id}/items/{item_id}": undefined,
  "POST /api/v2/admin/collections/import/mdblist": adminImport,
  "POST /api/v2/admin/collections/import/tmdb": adminImport,
  "POST /api/v2/admin/collections/import/tmdb-list": adminImport,
  "POST /api/v2/admin/collections/import/trakt": adminImport,
  "POST /api/v2/admin/collections/template-bundles/{bundle_id}/apply-job": adminTemplateJobAccepted,
  "GET /api/v2/admin/collection-jobs/{job_id}": getAdminTemplateJobCompleted,
  "GET /api/v2/collections": listCollectionsOk,
  "POST /api/v2/collections": createCollectionOk,
  "GET /api/v2/collections/{id}": getCollectionOk,
  "PATCH /api/v2/collections/{id}": getCollectionOk,
  "PUT /api/v2/collections/{id}/poster": getCollectionOk,
  "DELETE /api/v2/collections/{id}/image": undefined,
  "GET /api/v2/collections/{id}/items": getCollectionItemsOk,
  "PUT /api/v2/collections/{id}/items/{item_id}": undefined,
  "DELETE /api/v2/collections/{id}/items/{item_id}": undefined,
  "POST /api/v2/collections/import/mdblist": personalImport,
  "POST /api/v2/collections/import/tmdb": personalImport,
  "POST /api/v2/collections/import/tmdb-list": personalImport,
  "GET /api/v2/library/{id}/collections": getLibraryCollectionsOk,
  "POST /api/v2/catalog/query": queryCatalogItemsOk,
  "GET /api/v2/catalog/filters": getCatalogFiltersOk,
  "GET /api/v2/catalog/search/capabilities": getCatalogSearchCapabilitiesOk,
  "GET /api/v2/settings/overlay-config": getOverlayConfigOk,
  "GET /api/v2/capabilities/ratings": getRatingsCapabilityOk,
  "GET /api/v2/settings/contract/capabilities": getSettingsContractCapabilitiesOk,
};

const COLLECTION_ROOT = /^(\/api\/v2\/(?:admin\/)?collections\/[^/]+)\/.+$/;

function wireJSON(value: unknown): unknown {
  return value === undefined ? undefined : JSON.parse(JSON.stringify(value));
}

function wireForm(form: Record<string, Blob | string>): RecordedCall["form"] {
  return Object.fromEntries(
    Object.entries(form).map(([name, part]) => [
      name,
      typeof part === "string" ? part : { file: part instanceof File ? part.name : "blob" },
    ]),
  );
}

function fillPath(route: string, params: RequestOptions["path"]): string {
  return route.replace(/\{([^}]+)\}/g, (_match, name: string) => String(params?.[name] ?? ""));
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

export class V2Recorder {
  calls: RecordedCall[] = [];
  /** Operations a test sent that had neither a fixture nor an answer. */
  unanswered: string[] = [];
  private answers = new Map<string, Answer>();
  private revisions = new Map<string, number>();
  private collectionTypes = new Map<string, string>();
  private problemError?: typeof V2ProblemErrorClass;

  /** Called by `mockV2Request` with the real request module. */
  bind(problemError: typeof V2ProblemErrorClass) {
    this.problemError = problemError;
  }

  reset() {
    this.calls = [];
    this.unanswered = [];
    this.answers.clear();
    this.revisions.clear();
    this.collectionTypes.clear();
  }

  /** Answer an operation with a fixed value or a function of the call (which may throw). */
  answer(operation: string, answer: unknown) {
    this.answers.set(
      operation,
      typeof answer === "function" ? (answer as AnswerFn) : { value: answer },
    );
  }

  /** Tells the invariant check a collection's type when no read has shown it. */
  know(collection: { id: string; collection_type: string }) {
    this.collectionTypes.set(collection.id, collection.collection_type);
  }

  /**
   * The ETag a path currently answers with. It names the path, so a write
   * that sends another resource's ETag (the list's instead of the item's)
   * gets 412 and shows up in its golden.
   */
  etag(path: string): string {
    return `"${path}#${this.revisions.get(path) ?? 1}"`;
  }

  /** Changes a path's revision as if another writer saved it. */
  bump(path: string) {
    this.revisions.set(path, (this.revisions.get(path) ?? 1) + 1);
  }

  /** Every call that changes something, in order. */
  writes(): RecordedCall[] {
    return this.calls.filter((call) => isWrite(call.operation));
  }

  /** The calls of one operation, in order. */
  callsOf(operation: string): RecordedCall[] {
    return this.calls.filter((call) => call.operation === operation);
  }

  /** The operations in order, for asserting call order. */
  operations(filter: (call: RecordedCall) => boolean = () => true): string[] {
    return this.calls.filter(filter).map((call) => call.operation);
  }

  readonly v2 = async (operation: string, options: RequestOptions = {}): Promise<unknown> => {
    const call: RecordedCall = {
      operation,
      path: fillPath(operation.slice(operation.indexOf(" ") + 1), options.path),
      headers: { ...options.headers },
    };
    if (options.query !== undefined) call.query = wireJSON(options.query) as RecordedCall["query"];
    if (options.body !== undefined) call.body = wireJSON(options.body);
    if (options.form !== undefined) call.form = wireForm(options.form);
    this.calls.push(call);
    // Resolve on a later task, as a network answer would.
    await Promise.resolve();

    if (GUARDED_OPERATIONS.has(operation) && call.headers["If-Match"] !== this.etag(call.path)) {
      throw this.stale(operation, call.path);
    }
    const value = structuredClone(await this.resolve(call));
    if (isWrite(operation)) {
      this.bump(call.path);
      const root = COLLECTION_ROOT.exec(call.path)?.[1];
      if (root) this.bump(root);
    } else {
      this.learn(value);
    }
    options.onResponse?.(new Response(null, { headers: { ETag: this.etag(call.path) } }));
    return value;
  };

  private async resolve(call: RecordedCall): Promise<unknown> {
    const answer = this.answers.get(call.operation);
    if (answer) return "value" in answer ? answer.value : answer(call);
    if (call.operation in FIXTURE_ANSWERS) return FIXTURE_ANSWERS[call.operation];
    this.unanswered.push(call.operation);
    throw new Error(`v2Recorder: no answer for ${call.operation}`);
  }

  private stale(operation: string, path: string): Error {
    if (!this.problemError) throw new Error("v2Recorder: mockV2Request was not installed");
    return new this.problemError(
      operation,
      { ...adminCollectionStale },
      null,
      this.etag(path),
      new Headers({ ETag: this.etag(path) }),
    );
  }

  // Remembers each collection's type as the client read it.
  private learn(value: unknown) {
    const visit = (entry: unknown) => {
      if (Array.isArray(entry)) return entry.forEach(visit);
      if (!isRecord(entry)) return;
      if (typeof entry.id === "string" && typeof entry.collection_type === "string") {
        this.collectionTypes.set(entry.id, entry.collection_type);
      }
      Object.values(entry).forEach(visit);
    };
    visit(value);
  }

  /**
   * The rules every recorded collection request keeps, as messages:
   * a guarded write carries If-Match; top-level `library_id`/`library_ids`
   * are strings; an admin collection PATCH names its `collection_type`; a
   * manual collection's body never carries `query_definition` or
   * `sort_config`.
   */
  wireInvariantViolations(): string[] {
    const violations: string[] = [];
    for (const call of this.calls) {
      const label = `${call.operation} (${call.path})`;
      if (GUARDED_OPERATIONS.has(call.operation) && !call.headers["If-Match"]) {
        violations.push(`${label} has no If-Match`);
      }
      if (!isRecord(call.body)) continue;
      const body = call.body;
      if ("library_ids" in body) {
        const ids = body.library_ids;
        if (!Array.isArray(ids) || ids.some((id) => typeof id !== "string")) {
          violations.push(`${label} sends library_ids that are not strings`);
        }
      }
      if ("library_id" in body && typeof body.library_id !== "string") {
        violations.push(`${label} sends a library_id that is not a string`);
      }
      if (call.operation === "PATCH /api/v2/admin/collections/{id}" && !body.collection_type) {
        violations.push(`${label} does not name collection_type`);
      }
      const id = call.path.split("/").at(-1) ?? "";
      const manual =
        body.collection_type === "manual" ||
        (call.operation.startsWith("PATCH") && this.collectionTypes.get(id) === "manual");
      if (manual && ("query_definition" in body || "sort_config" in body)) {
        violations.push(`${label} sends query_definition or sort_config for a manual collection`);
      }
    }
    return violations;
  }
}

function isWrite(operation: string): boolean {
  return !operation.startsWith("GET ") && !READ_ONLY_POSTS.has(operation);
}

export const v2Recorder = new V2Recorder();

/** The `vi.mock("@/api/v2/request")` factory: the real module with `v2` recorded. */
export async function mockV2Request() {
  const actual = await vi.importActual<typeof import("@/api/v2/request")>("@/api/v2/request");
  v2Recorder.bind(actual.V2ProblemError);
  return { ...actual, v2: v2Recorder.v2 };
}

/**
 * Resets the recorder before each test; after it, unmounts what the test
 * rendered (so no late request lands in the next test) and checks the wire
 * invariants.
 */
export function installV2Recorder() {
  beforeEach(() => v2Recorder.reset());
  afterEach(() => {
    cleanup();
    expect(v2Recorder.unanswered, "operations without an answer").toEqual([]);
    expect(v2Recorder.wireInvariantViolations()).toEqual([]);
  });
}
