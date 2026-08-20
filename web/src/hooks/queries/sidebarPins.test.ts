import { describe, expect, it } from "vitest";
import {
  createSidebarPinsOptimisticMutation,
  parseSidebarPins,
  rollbackSidebarPinsOptimisticMutation,
  setNavigationShortcutPresence,
  sidebarPinsToShortcuts,
  toggleNavigationShortcut,
  toggleSidebarPins,
} from "./sidebarPins";

describe("sidebar pin helpers", () => {
  it("parses invalid values as an empty pin map", () => {
    expect(parseSidebarPins(null)).toEqual({});
    expect(parseSidebarPins(undefined)).toEqual({});
    expect(parseSidebarPins("not-json")).toEqual({});
    expect(parseSidebarPins("[]")).toEqual({});
    expect(parseSidebarPins([])).toEqual({});
    expect(parseSidebarPins(42)).toEqual({});
  });

  it("accepts the canonical object value directly", () => {
    const pins = {
      "42": [{ type: "collection", id: "col-1", label: "Pinned Horror" }],
    };
    expect(parseSidebarPins(pins)).toEqual(pins);
  });

  it("still accepts the legacy JSON-string encoding", () => {
    expect(
      parseSidebarPins('{"42":[{"type":"collection","id":"col-1","label":"Pinned Horror"}]}'),
    ).toEqual({
      "42": [{ type: "collection", id: "col-1", label: "Pinned Horror" }],
    });
  });

  it("groups the canonical cross-client shortcut catalog for the web sidebar", () => {
    expect(
      parseSidebarPins({
        items: [
          { type: "library", library_id: 42, label: "Movies" },
          { type: "section", library_id: 42, section_id: "recent", label: "Recent" },
          {
            type: "collection",
            library_id: 42,
            collection_id: "horror",
            label: "Horror",
          },
          { type: "collection", collection_id: "global", label: "Global" },
        ],
      }),
    ).toEqual({
      "42": [
        { type: "section", id: "recent", label: "Recent" },
        { type: "collection", id: "horror", label: "Horror" },
      ],
    });
  });

  it("serializes grouped sidebar pins into canonical shortcuts", () => {
    expect(
      sidebarPinsToShortcuts({
        "42": [
          { type: "section", id: "recent", label: "Recent" },
          { type: "collection", id: "horror", label: "Horror" },
        ],
      }),
    ).toEqual({
      items: [
        { type: "section", library_id: 42, section_id: "recent", label: "Recent" },
        {
          type: "collection",
          library_id: 42,
          collection_id: "horror",
          label: "Horror",
        },
      ],
    });
  });

  it("toggles one sidebar target without dropping other shared shortcuts", () => {
    const current = {
      items: [
        { type: "library", library_id: 42, label: "Movies" },
        { type: "collection", collection_id: "global", label: "Global" },
      ],
    };
    expect(
      toggleNavigationShortcut(current, 42, {
        type: "section",
        id: "recent",
        label: "Recent",
      }),
    ).toEqual({
      items: [
        { type: "library", library_id: 42, label: "Movies" },
        { type: "collection", collection_id: "global", label: "Global" },
        { type: "section", library_id: 42, section_id: "recent", label: "Recent" },
      ],
    });
  });

  it("sets desired presence and refreshes a label without reordering", () => {
    const target = {
      type: "section" as const,
      library_id: 42,
      section_id: "recent",
      label: "Recently Added",
    };
    const current = {
      items: [
        { type: "library", library_id: 42, label: "Movies" },
        { ...target, label: "Recent" },
      ],
    };

    expect(setNavigationShortcutPresence(current, target, true)).toEqual({
      items: [{ type: "library", library_id: 42, label: "Movies" }, target],
    });
    expect(setNavigationShortcutPresence(current, target, false)).toEqual({
      items: [{ type: "library", library_id: 42, label: "Movies" }],
    });
  });

  it("adds a new pin to the target library", () => {
    expect(
      toggleSidebarPins({}, 42, { type: "collection", id: "col-1", label: "Pinned Horror" }),
    ).toEqual({
      "42": [{ type: "collection", id: "col-1", label: "Pinned Horror" }],
    });
  });

  it("removes an existing pin from the target library only", () => {
    expect(
      toggleSidebarPins(
        {
          "42": [
            { type: "collection", id: "col-1", label: "Pinned Horror" },
            { type: "section", id: "sec-1", label: "Recently Added" },
          ],
          "99": [{ type: "collection", id: "col-2", label: "Other Library" }],
        },
        42,
        { type: "collection", id: "col-1", label: "Pinned Horror" },
      ),
    ).toEqual({
      "42": [{ type: "section", id: "sec-1", label: "Recently Added" }],
      "99": [{ type: "collection", id: "col-2", label: "Other Library" }],
    });
  });

  it("builds the optimistic value as a typed object, not a JSON string", () => {
    const mutation = createSidebarPinsOptimisticMutation({
      currentValue: { "42": [{ type: "section", id: "sec-1", label: "Recently Added" }] },
      currentRevision: null,
      libraryId: 42,
      pin: { type: "collection", id: "col-1", label: "Pinned Horror" },
      revision: 1,
    });

    expect(mutation.optimisticValue).toEqual({
      "42": [
        { type: "section", id: "sec-1", label: "Recently Added" },
        { type: "collection", id: "col-1", label: "Pinned Horror" },
      ],
    });
  });

  it("rolls back the latest optimistic mutation when its revision is still current", () => {
    const mutation = createSidebarPinsOptimisticMutation({
      currentValue: null,
      currentRevision: null,
      libraryId: 42,
      pin: { type: "collection", id: "col-1", label: "Pinned Horror" },
      revision: 1,
    });

    expect(
      rollbackSidebarPinsOptimisticMutation({
        currentRevision: 1,
        mutation,
      }),
    ).toEqual({
      value: null,
      revision: null,
    });
  });

  it("does not roll back over a newer optimistic mutation revision", () => {
    const firstMutation = createSidebarPinsOptimisticMutation({
      currentValue: null,
      currentRevision: null,
      libraryId: 42,
      pin: { type: "collection", id: "col-1", label: "Pinned Horror" },
      revision: 1,
    });
    createSidebarPinsOptimisticMutation({
      currentValue: firstMutation.optimisticValue,
      currentRevision: 1,
      libraryId: 42,
      pin: { type: "section", id: "sec-1", label: "Recently Added" },
      revision: 2,
    });

    expect(
      rollbackSidebarPinsOptimisticMutation({
        currentRevision: 2,
        mutation: firstMutation,
      }),
    ).toBeNull();
  });
});
