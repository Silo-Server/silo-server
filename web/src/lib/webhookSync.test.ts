// @vitest-environment node

import { describe, expect, it } from "vitest";
import { buildLegacyWebhookSyncRedirectTarget } from "./webhookSync";

describe("buildLegacyWebhookSyncRedirectTarget", () => {
  it("preserves callback query parameters from the legacy Plex settings route", () => {
    expect(
      buildLegacyWebhookSyncRedirectTarget("?plex_auth=1&plex_pin_id=123&plex_pin_code=abc"),
    ).toBe("/settings/webhook-sync?plex_auth=1&plex_pin_id=123&plex_pin_code=abc");
  });

  it("falls back to the generic settings route without a query string", () => {
    expect(buildLegacyWebhookSyncRedirectTarget("")).toBe("/settings/webhook-sync");
  });
});
