import { bulkCreateAdminSections, createAdminSection } from "@/api/adminSections";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { canCopyToLibraries } from "./bulkCopy";
import { buildBulkCopyPayload, buildRowCreateRequest } from "./payloads";
import { everyPreset } from "./recipeCatalogFixture.test-support";
import { draftForPreset } from "./rowDraft";

const wire = vi.hoisted(() => ({ last: undefined as unknown }));
vi.mock("@/api/v2/request", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/v2/request")>()),
  v2: vi.fn(async (route: string, init?: { body?: unknown }) => {
    wire.last = { route, body: init?.body };
    throw new Error("request captured");
  }),
}));

beforeEach(() => {
  wire.last = undefined;
});

/** The JSON a send puts on the wire: undefined members are dropped, as fetch drops them. */
async function sent(send: () => Promise<unknown>): Promise<{ route: string; body: unknown }> {
  wire.last = undefined;
  await expect(send()).rejects.toThrow("request captured");
  return JSON.parse(JSON.stringify(wire.last));
}

const LIBRARY_ID = 7;
describe("admin bulk create", () => {
  it("sends an Add row draft's library copies with the single create body apart from the libraries", async () => {
    for (const { def, preset } of everyPreset()) {
      const draft = draftForPreset(def, preset);
      if (!canCopyToLibraries(draft)) continue;
      const page = { kind: "library", libraryId: LIBRARY_ID } as const;
      const single = await sent(() =>
        createAdminSection(buildRowCreateRequest(draft, draft.title, page, 3)),
      );
      const bulk = await sent(() =>
        bulkCreateAdminSections(buildBulkCopyPayload({ ...draft, enabled: true }, [LIBRARY_ID, 8])),
      );
      const {
        library_id: _library,
        position: _position,
        ...singleRest
      } = single.body as Record<string, unknown>;
      const { library_ids: bulkLibraries, ...bulkRest } = bulk.body as Record<string, unknown>;
      expect(bulk.route).toBe("POST /api/v2/admin/sections/bulk");
      expect(bulkLibraries).toEqual([String(LIBRARY_ID), "8"]);
      expect(bulkRest, `${def.type}/${preset.key}`).toEqual(singleRest);
    }
  });
});
