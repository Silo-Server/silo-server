import { V2ProblemError } from "@/api/v2/request";
import { describe, expect, it, vi } from "vitest";
import { createAdminSectionCreation, runAdminSectionCreation } from "./adminSectionCreation";

function rejection(detail: string, status = 422): V2ProblemError {
  return new V2ProblemError("create_admin_section", {
    type: "https://silo.example/problems/validation_failed",
    title: "Validation failed",
    instance: "/api/v2/admin/sections",
    status,
    detail,
  });
}

const payload = {
  section_type: "collection",
  title: "Trending",
  item_limit: 20,
  featured: false,
  enabled: true,
  config: { trakt_preset: "trending" },
};

describe("admin section creation progress", () => {
  it("publishes an imported ID before a failed create and retries only that create", async () => {
    const resolveCollection = vi.fn().mockResolvedValue("collection-1");
    const createSection = vi
      .fn()
      .mockRejectedValueOnce(rejection("Create failed"))
      .mockResolvedValueOnce("section-1");
    const onProgress = vi.fn();
    const initial = createAdminSectionCreation(payload, [1]);
    const failed = await runAdminSectionCreation(initial, {
      resolveCollection,
      createSection,
      onProgress,
    });
    expect(failed.targets[0]).toMatchObject({
      collectionID: "collection-1",
      status: "section_failed",
      error: "Create failed",
    });
    expect(
      onProgress.mock.calls.some(
        ([state]) =>
          state.targets[0].collectionID === "collection-1" &&
          state.targets[0].status === "creating",
      ),
    ).toBe(true);
    const done = await runAdminSectionCreation(failed, { resolveCollection, createSection });
    expect(resolveCollection).toHaveBeenCalledTimes(1);
    expect(createSection).toHaveBeenCalledTimes(2);
    expect(done.targets[0]).toMatchObject({
      collectionID: "collection-1",
      sectionID: "section-1",
      status: "complete",
    });
    expect(initial.targets[0]?.status).toBe("pending");
  });

  it("keeps earlier completed libraries when a later section fails", async () => {
    const resolveCollection = vi.fn(async (_payload, id: number) => `collection-${id}`);
    const createSection = vi
      .fn()
      .mockResolvedValueOnce("section-1")
      .mockRejectedValueOnce(rejection("Second section failed"))
      .mockResolvedValueOnce("section-2")
      .mockResolvedValueOnce("section-3");
    const failed = await runAdminSectionCreation(createAdminSectionCreation(payload, [1, 2, 3]), {
      resolveCollection,
      createSection,
    });
    expect(failed.targets.map((target) => target.status)).toEqual([
      "complete",
      "section_failed",
      "pending",
    ]);
    const done = await runAdminSectionCreation(failed, { resolveCollection, createSection });
    expect(done.targets.map((target) => target.status)).toEqual([
      "complete",
      "complete",
      "complete",
    ]);
    expect(resolveCollection.mock.calls.map((call) => call[1])).toEqual([1, 2, 3]);
    expect(createSection.mock.calls.map((call) => call[1])).toEqual([1, 2, 2, 3]);
  });

  it("never repeats an import with an unknown outcome on retry remaining", async () => {
    const resolveCollection = vi
      .fn()
      .mockRejectedValueOnce(new Error("Connection lost"))
      .mockResolvedValueOnce("collection-2");
    const createSection = vi.fn().mockResolvedValue("section-2");
    const failed = await runAdminSectionCreation(createAdminSectionCreation(payload, [1, 2]), {
      resolveCollection,
      createSection,
    });
    expect(failed.targets[0]?.status).toBe("import_unknown");
    expect(createSection).not.toHaveBeenCalled();
    const remaining = await runAdminSectionCreation(failed, { resolveCollection, createSection });
    expect(resolveCollection.mock.calls.map((call) => call[1])).toEqual([1, 2]);
    expect(remaining.targets.map((target) => target.status)).toEqual([
      "import_unknown",
      "complete",
    ]);
  });

  it("keeps completed targets when a later import outcome is unknown", async () => {
    const resolveCollection = vi
      .fn()
      .mockResolvedValueOnce("collection-1")
      .mockRejectedValueOnce(new Error("Import response lost"))
      .mockResolvedValueOnce("collection-3");
    const createSection = vi
      .fn()
      .mockResolvedValueOnce("section-1")
      .mockResolvedValueOnce("section-3");
    const failed = await runAdminSectionCreation(createAdminSectionCreation(payload, [1, 2, 3]), {
      resolveCollection,
      createSection,
    });
    expect(failed.targets.map((target) => target.status)).toEqual([
      "complete",
      "import_unknown",
      "pending",
    ]);
    const done = await runAdminSectionCreation(failed, { resolveCollection, createSection });
    expect(done.targets[0]?.sectionID).toBe("section-1");
    expect(done.targets.map((target) => target.status)).toEqual([
      "complete",
      "import_unknown",
      "complete",
    ]);
    expect(resolveCollection.mock.calls.map((call) => call[1])).toEqual([1, 2, 3]);
    expect(createSection.mock.calls.map((call) => call[1])).toEqual([1, 3]);
  });

  it.each([new Error("Response lost"), rejection("Internal error", 500)])(
    "does not replay a section create with an unknown outcome: %s",
    async (error) => {
      const resolveCollection = vi.fn(async (_payload, id: number) => `collection-${id}`);
      const createSection = vi.fn().mockRejectedValueOnce(error).mockResolvedValueOnce("section-2");
      const failed = await runAdminSectionCreation(createAdminSectionCreation(payload, [1, 2]), {
        resolveCollection,
        createSection,
      });
      expect(failed.targets[0]).toMatchObject({
        collectionID: "collection-1",
        status: "section_unknown",
      });
      const remaining = await runAdminSectionCreation(failed, { resolveCollection, createSection });
      expect(remaining.targets.map((target) => target.status)).toEqual([
        "section_unknown",
        "complete",
      ]);
      expect(resolveCollection.mock.calls.map((call) => call[1])).toEqual([1, 2]);
      expect(createSection.mock.calls.map((call) => call[1])).toEqual([1, 2]);
    },
  );

  it("captures the submitted config and limits one attempt to 100 libraries", () => {
    const draft = structuredClone(payload);
    const state = createAdminSectionCreation(draft, [1, 1]);
    draft.title = "Changed";
    draft.config.trakt_preset = "popular";
    expect(state.payload.title).toBe("Trending");
    expect(state.payload.config.trakt_preset).toBe("trending");
    expect(state.targets).toHaveLength(1);
    expect(() => createAdminSectionCreation(payload, [])).toThrow();
    expect(() => createAdminSectionCreation(payload, [0])).toThrow();
    expect(() =>
      createAdminSectionCreation(
        payload,
        Array.from({ length: 101 }, (_, i) => i + 1),
      ),
    ).toThrow();
  });
});
