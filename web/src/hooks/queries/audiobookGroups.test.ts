import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import listAudiobookGroupsOk from "../../../../contracts/api/v2/fixtures/list_audiobook_groups_ok.json";

import { setProfileId } from "@/api/client";
import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";

import { fetchAudiobookGroupsPage } from "./audiobookGroups";

describe("audiobook groups on the v2 contract", () => {
  beforeEach(() => {
    installPolicyStorageMocks();
    setProfileId("p-owner");
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("requests the first page with an exact total and returns the cursor for the next", async () => {
    const fetchMock = vi.fn<typeof fetch>(async () => jsonResponse(listAudiobookGroupsOk));
    vi.stubGlobal("fetch", fetchMock);

    const page = await fetchAudiobookGroupsPage(3, "author", "name", "", true, "  fra ");

    const url = new URL(String(fetchMock.mock.calls[0]?.[0]), "http://localhost");
    expect(url.pathname).toBe("/api/v2/catalog/audiobook-groups");
    expect(url.searchParams.get("library_id")).toBe("3");
    expect(url.searchParams.get("group_by")).toBe("author");
    expect(url.searchParams.get("sort")).toBe("name");
    expect(url.searchParams.get("limit")).toBe("60");
    expect(url.searchParams.get("q")).toBe("fra");
    expect(url.searchParams.has("cursor")).toBe(false);
    expect(url.searchParams.has("skip_total")).toBe(false);
    expect(page.groups.map((g) => g.name)).toEqual(["Frank Herbert"]);
    expect(page.total).toBe(2);
    expect(page.total_exact).toBe(true);
    expect(page.has_more).toBe(true);
    expect(page.next_cursor).toBe(listAudiobookGroupsOk.page.next_cursor);
  });

  it("resumes later pages from the cursor and skips the count", async () => {
    const fetchMock = vi.fn<typeof fetch>(async () =>
      jsonResponse({ ...listAudiobookGroupsOk, page: { has_more: false } }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const page = await fetchAudiobookGroupsPage(3, "narrator", "count", "cursor-2", false, "");

    const url = new URL(String(fetchMock.mock.calls[0]?.[0]), "http://localhost");
    expect(url.searchParams.get("cursor")).toBe("cursor-2");
    expect(url.searchParams.get("skip_total")).toBe("true");
    expect(url.searchParams.has("q")).toBe(false);
    expect(page.has_more).toBe(false);
    expect(page.next_cursor).toBeUndefined();
  });
});
