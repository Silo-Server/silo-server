import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import getRatingsCapabilityOk from "../../../../contracts/api/v2/fixtures/get_ratings_capability_ok.json";

import { setProfileId } from "@/api/client";
import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";

import { fetchShownRatingSources } from "./ratingsCapability";

describe("ratings capability on the v2 contract", () => {
  beforeEach(() => {
    installPolicyStorageMocks();
    setProfileId("p-owner");
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("lists the sources the server shows", async () => {
    const fetchMock = vi.fn<typeof fetch>(async () => jsonResponse(getRatingsCapabilityOk));
    vi.stubGlobal("fetch", fetchMock);

    await expect(fetchShownRatingSources()).resolves.toEqual(["imdb", "tmdb"]);
    expect(String(fetchMock.mock.calls[0]?.[0])).toBe("/api/v2/capabilities/ratings");
  });

  it("shows nothing when the capability is not available", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async () =>
        jsonResponse({ ...getRatingsCapabilityOk, state: "unsupported" }),
      ),
    );

    await expect(fetchShownRatingSources()).resolves.toEqual([]);
  });
});
