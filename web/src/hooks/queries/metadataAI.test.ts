import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import getMetadataAICapabilityOk from "../../../../contracts/api/v2/fixtures/get_metadata_ai_capability_ok.json";

import { setProfileId } from "@/api/client";
import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";

import { fetchMetadataAIStatus } from "./metadataAI";

describe("metadata AI capability on the v2 contract", () => {
  beforeEach(() => {
    installPolicyStorageMocks();
    setProfileId("p-owner");
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("maps an available capability onto the enabled flag and on-view mode", async () => {
    const fetchMock = vi.fn<typeof fetch>(async () => jsonResponse(getMetadataAICapabilityOk));
    vi.stubGlobal("fetch", fetchMock);

    await expect(fetchMetadataAIStatus()).resolves.toEqual({ enabled: true, on_view: "button" });
    expect(String(fetchMock.mock.calls[0]?.[0])).toBe("/api/v2/capabilities/metadata-ai");
  });

  it("treats every other state as disabled with on-view off", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async () =>
        jsonResponse({ ...getMetadataAICapabilityOk, state: "not_configured", on_view: "off" }),
      ),
    );

    await expect(fetchMetadataAIStatus()).resolves.toEqual({ enabled: false, on_view: "off" });
  });
});
