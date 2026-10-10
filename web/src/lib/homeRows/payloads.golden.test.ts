import { bulkCreateAdminSections, createAdminSection } from "@/api/adminSections";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { canCopyToLibraries } from "./bulkCopy";
import { pickerGroups } from "./catalog";
import { buildBulkCopyPayload, buildRowCreateRequest } from "./payloads";
import {
  everyPreset,
  everyRecipe,
  recipeCatalogFixture,
} from "./recipeCatalogFixture.test-support";
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

  it("copies an existing row as it is, never as a hero banner", async () => {
    const copy = await sent(() =>
      bulkCreateAdminSections(
        buildBulkCopyPayload(
          {
            sectionType: "trending_on_server",
            title: "Trending This Week",
            itemLimit: 30,
            config: { window: "7d" },
            enabled: false,
          },
          [8, 9],
        ),
      ),
    );
    expect(copy).toEqual({
      route: "POST /api/v2/admin/sections/bulk",
      body: {
        scope: "library",
        library_ids: ["8", "9"],
        section_type: "trending_on_server",
        title: "Trending This Week",
        item_limit: 30,
        featured: false,
        enabled: false,
        config: { window: "7d" },
      },
    });
  });
});

// Both pickers share one list: the server lets every profile add rule rows
// and refuses only new rows of an admin-only kind from a profile.
describe("rule rows for every profile", () => {
  it("offers rule rows and no admin-only kind", () => {
    const offered = pickerGroups(recipeCatalogFixture).flatMap((group) =>
      group.cards.map((card) => card.type),
    );
    expect(offered).toContain("custom_filter");
    const adminOnly = everyRecipe()
      .filter((def) => def.admin_only)
      .map((def) => def.type);
    expect(adminOnly).toEqual(["admin_curated_list"]);
    expect(offered.some((type) => adminOnly.includes(type))).toBe(false);
  });
});
