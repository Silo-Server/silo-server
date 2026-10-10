import { beforeEach, describe, expect, it, vi } from "vitest";

import getCollectionOk from "../../../contracts/api/v2/fixtures/get_collection_ok.json";
import { v2, V2ProblemError } from "@/api/v2/request";
import { v2Recorder } from "./v2Recorder";

vi.mock("@/api/v2/request", async () => (await import("./v2Recorder")).mockV2Request());

// These tests break the invariants on purpose, so they reset the recorder
// themselves instead of installing its after-test checks.
beforeEach(() => v2Recorder.reset());

function etagOf(read: (onResponse: (response: Response) => void) => Promise<unknown>) {
  let etag: string | null = null;
  return read((response) => (etag = response.headers.get("ETag"))).then(() => etag);
}

describe("v2Recorder", () => {
  it("answers from the contract fixtures and records the body as sent", async () => {
    const created = await v2("POST /api/v2/collections", {
      body: { name: "Rainy days", collection_type: "manual", query_definition: undefined },
    });

    expect(created).toEqual(getCollectionOk);
    expect(v2Recorder.calls).toEqual([
      {
        operation: "POST /api/v2/collections",
        path: "/api/v2/collections",
        headers: {},
        body: { name: "Rainy days", collection_type: "manual" },
      },
    ]);
  });

  it("answers a guarded write with a stale If-Match 412, with the current ETag", async () => {
    const etag = await etagOf((onResponse) =>
      v2("GET /api/v2/collections/{id}", { path: { id: "c1" }, onResponse }),
    );
    v2Recorder.bump("/api/v2/collections/c1");

    const stale = await v2("PATCH /api/v2/collections/{id}", {
      path: { id: "c1" },
      headers: { "If-Match": etag! },
      body: { name: "Renamed" },
    }).catch((error: unknown) => error);

    expect(stale).toBeInstanceOf(V2ProblemError);
    expect((stale as V2ProblemError).status).toBe(412);
    expect((stale as V2ProblemError).currentETag).toBe('"/api/v2/collections/c1#2"');
  });

  it("answers 412 to a guarded write that sends another resource's ETag", async () => {
    const listETag = await etagOf((onResponse) => v2("GET /api/v2/collections", { onResponse }));

    const wrong = await v2("PATCH /api/v2/collections/{id}", {
      path: { id: "c1" },
      headers: { "If-Match": listETag! },
      body: { name: "Renamed" },
    }).catch((error: unknown) => error);

    expect(listETag).not.toBe(v2Recorder.etag("/api/v2/collections/c1"));
    expect((wrong as V2ProblemError).status).toBe(412);
  });

  it("moves a collection's ETag when one of its items changes", async () => {
    expect(v2Recorder.etag("/api/v2/collections/c1")).toBe('"/api/v2/collections/c1#1"');
    await v2("PUT /api/v2/collections/{id}/items/{item_id}", {
      path: { id: "c1", item_id: "movie:heat-1995" },
      body: { position: 0 },
    });
    expect(v2Recorder.etag("/api/v2/collections/c1")).toBe('"/api/v2/collections/c1#2"');
  });

  it("fails an operation it has no answer for and remembers it", async () => {
    await expect(v2("GET /api/v2/collections/server")).rejects.toThrow("no answer");
    expect(v2Recorder.unanswered).toEqual(["GET /api/v2/collections/server"]);
  });

  it("reports requests that break the collection wire rules", async () => {
    v2Recorder.answer("PATCH /api/v2/admin/collections/{id}", {});
    v2Recorder.answer("DELETE /api/v2/collections/{id}", undefined);
    v2Recorder.know({ id: "c1", collection_type: "manual" });
    const send = (operation: string, options: object) =>
      (v2 as unknown as (operation: string, options: object) => Promise<unknown>)(
        operation,
        options,
      ).catch(() => undefined);

    await send("DELETE /api/v2/collections/{id}", { path: { id: "c2" } });
    await send("POST /api/v2/admin/collections", {
      body: { collection_type: "manual", library_ids: [1], sort_config: {} },
    });
    await send("PATCH /api/v2/admin/collections/{id}", {
      path: { id: "c3" },
      headers: { "If-Match": v2Recorder.etag("/api/v2/admin/collections/c3") },
      body: { title: "No type" },
    });
    await send("PATCH /api/v2/collections/{id}", {
      path: { id: "c1" },
      headers: { "If-Match": v2Recorder.etag("/api/v2/collections/c1") },
      body: { query_definition: {} },
    });

    expect(v2Recorder.wireInvariantViolations()).toEqual([
      "DELETE /api/v2/collections/{id} (/api/v2/collections/c2) has no If-Match",
      "POST /api/v2/admin/collections (/api/v2/admin/collections) sends library_ids that are not strings",
      "POST /api/v2/admin/collections (/api/v2/admin/collections) sends query_definition or sort_config for a manual collection",
      "PATCH /api/v2/admin/collections/{id} (/api/v2/admin/collections/c3) does not name collection_type",
      "PATCH /api/v2/collections/{id} (/api/v2/collections/c1) sends query_definition or sort_config for a manual collection",
    ]);
  });
});
