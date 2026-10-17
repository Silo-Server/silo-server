import { describe, expect, it } from "vitest";

import { localWebhookURL, normalizeLocalWebhookBase } from "./webhookURL";

describe("local webhook base", () => {
  it.each([
    ["192.168.1.10:8080", "http://192.168.1.10:8080"],
    ["http://192.168.1.10:8080/", "http://192.168.1.10:8080"],
    ["https://silo.lan/silo/", "https://silo.lan/silo"],
    ["  http://tower:8080  ", "http://tower:8080"],
    ["", ""],
    ["ftp://192.168.1.10", ""],
    ["http://", ""],
  ])("normalizes %j", (input, expected) => {
    expect(normalizeLocalWebhookBase(input)).toBe(expected);
  });
});

describe("local webhook URL", () => {
  it.each([
    [
      "https://silo.example.test/api/v2/autoscan/webhooks/secret",
      "http://192.168.1.10:8080",
      "http://192.168.1.10:8080/api/v2/autoscan/webhooks/secret",
    ],
    [
      "https://example.test/silo/api/v1/autoscan/webhooks/token%2Bvalue",
      "192.168.1.10:8080",
      "http://192.168.1.10:8080/api/v2/autoscan/webhooks/token%2Bvalue",
    ],
    [
      "/api/v2/autoscan/webhooks/secret?deployment=one",
      "http://tower:8080/silo",
      "http://tower:8080/silo/api/v2/autoscan/webhooks/secret?deployment=one",
    ],
    ["https://silo.example.test/api/v2/autoscan/webhooks/secret", "", ""],
    ["https://silo.example.test/api/v1/unrelated/secret", "http://tower:8080", ""],
  ])("rebases %s onto %j", (url, base, expected) => {
    expect(localWebhookURL(url, base)).toBe(expected);
  });
});
