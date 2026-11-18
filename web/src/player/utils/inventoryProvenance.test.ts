import { describe, expect, it } from "vitest";
import {
  PROVISIONAL_INVENTORY_POLL_MS,
  PROVISIONAL_INVENTORY_SLOW_MS,
  PROVISIONAL_INVENTORY_TIMEOUT_MS,
  isInventoryProvisional,
  isOverlayInventoryProvisional,
  isPlanSubtitleInventoryProvisional,
  isProvisionalInventoryExpired,
  isProvisionalInventorySlow,
} from "./inventoryProvenance";

describe("isInventoryProvisional", () => {
  it("is provisional while the stamp-derived status is declared", () => {
    expect(isInventoryProvisional("declared", "")).toBe(true);
    expect(isInventoryProvisional("declared", undefined)).toBe(true);
  });

  it("is confident once the status is verified", () => {
    expect(isInventoryProvisional("verified", "")).toBe(false);
  });

  it("treats a declared or pending resolve as provisional even when the stamp reads verified", () => {
    expect(isInventoryProvisional("verified", "declared")).toBe(true);
    expect(isInventoryProvisional("verified", "pending")).toBe(true);
  });

  it("keeps a failed probe provisional because declared metadata is served", () => {
    expect(isInventoryProvisional("declared", "failed")).toBe(true);
  });

  it("trusts a verified resolve over a declared stamp", () => {
    expect(isInventoryProvisional("declared", "verified")).toBe(false);
  });

  it("stays confident when an older server publishes neither field", () => {
    expect(isInventoryProvisional(undefined, undefined)).toBe(false);
    expect(isInventoryProvisional("", "")).toBe(false);
  });

  it("treats tracks_pending as provisional even over verified stamp and provenance", () => {
    expect(isInventoryProvisional("verified", "verified", true)).toBe(true);
    expect(isInventoryProvisional("declared", "declared", true)).toBe(true);
    expect(isInventoryProvisional(undefined, undefined, true)).toBe(true);
  });

  it("ignores an absent or false tracks_pending flag", () => {
    expect(isInventoryProvisional("verified", "verified", false)).toBe(false);
    expect(isInventoryProvisional("verified", "verified", undefined)).toBe(false);
    expect(isInventoryProvisional("verified", "verified", null)).toBe(false);
  });
});

describe("isPlanSubtitleInventoryProvisional", () => {
  it("treats the server's inventory_unavailable answer as provisional", () => {
    expect(
      isPlanSubtitleInventoryProvisional({
        inventory_status: "verified",
        inventory_provenance: "verified",
        claims: { subtitles: { reason: "inventory_unavailable" } },
      }),
    ).toBe(true);
  });

  it("stays provisional for tracks_pending and declared provenance", () => {
    expect(isPlanSubtitleInventoryProvisional({ tracks_pending: true })).toBe(true);
    expect(
      isPlanSubtitleInventoryProvisional({
        inventory_status: "verified",
        inventory_provenance: "declared",
      }),
    ).toBe(true);
  });

  it("is confident for a verified inventory with no unavailable reason", () => {
    expect(
      isPlanSubtitleInventoryProvisional({
        inventory_status: "verified",
        inventory_provenance: "verified",
        claims: { subtitles: {} },
      }),
    ).toBe(false);
  });
});

describe("isOverlayInventoryProvisional", () => {
  it("shows declared rows immediately instead of waiting for the probe", () => {
    expect(isOverlayInventoryProvisional(true, 2)).toBe(false);
  });

  it("stays provisional only while the plan reports no rows at all", () => {
    expect(isOverlayInventoryProvisional(true, 0)).toBe(true);
  });

  it("keeps an empty verified inventory final so the menu can show its empty state", () => {
    expect(isOverlayInventoryProvisional(false, 0)).toBe(false);
  });

  it("treats probed rows as final", () => {
    expect(isOverlayInventoryProvisional(false, 2)).toBe(false);
  });
});

describe("isProvisionalInventoryExpired", () => {
  const since = 1_000_000;

  it("keeps the spinner inside the discovery window", () => {
    expect(isProvisionalInventoryExpired(true, since, since + 1)).toBe(false);
    expect(
      isProvisionalInventoryExpired(true, since, since + PROVISIONAL_INVENTORY_TIMEOUT_MS - 1),
    ).toBe(false);
  });

  it("offers a retry once the window lapses", () => {
    expect(
      isProvisionalInventoryExpired(true, since, since + PROVISIONAL_INVENTORY_TIMEOUT_MS),
    ).toBe(true);
    expect(
      isProvisionalInventoryExpired(true, since, since + PROVISIONAL_INVENTORY_TIMEOUT_MS + 5_000),
    ).toBe(true);
  });

  it("never expires a menu that is not provisional", () => {
    expect(isProvisionalInventoryExpired(false, since, since + 60_000)).toBe(false);
    expect(isProvisionalInventoryExpired(true, null, since + 60_000)).toBe(false);
  });

  it("honors a caller's shorter bound", () => {
    expect(isProvisionalInventoryExpired(true, since, since + 2_000, 2_000)).toBe(true);
    expect(isProvisionalInventoryExpired(true, since, since + 1_999, 2_000)).toBe(false);
  });
});

// The client's wait must follow the server's answer, not cut it short. The
// server bounds a selection probe chain at 20 s (virtualSelectionProbeBudget),
// so a slow-but-successful selection that answers at 16-18 s has to still be
// spinning when it does: the previous 15 s window declared "Not verified" while
// the server was still working, which is what the production measurement caught.
describe("the provisional window follows the server's selection bound", () => {
  const since = 1_000_000;

  it("is still waiting at 15 s, past the old client-only deadline", () => {
    expect(isProvisionalInventoryExpired(true, since, since + 15_000)).toBe(false);
    expect(isProvisionalInventoryExpired(true, since, since + 16_000)).toBe(false);
    expect(isProvisionalInventoryExpired(true, since, since + 18_000)).toBe(false);
  });

  it("outlasts the server's own chain bound", () => {
    // Mirrors virtualSelectionProbeBudget in
    // internal/api/handlers/playback_virtual_probe_chain.go.
    const serverSelectionProbeBudgetMs = 20_000;
    expect(PROVISIONAL_INVENTORY_TIMEOUT_MS).toBeGreaterThan(serverSelectionProbeBudgetMs);
    expect(isProvisionalInventoryExpired(true, since, since + serverSelectionProbeBudgetMs)).toBe(
      false,
    );
  });

  it("is slow, but not expired, between the two thresholds", () => {
    expect(isProvisionalInventorySlow(true, since, since + 1_000)).toBe(false);
    expect(isProvisionalInventorySlow(true, since, since + PROVISIONAL_INVENTORY_SLOW_MS)).toBe(
      true,
    );
    expect(isProvisionalInventoryExpired(true, since, since + PROVISIONAL_INVENTORY_SLOW_MS)).toBe(
      false,
    );
  });

  it("polls often enough that the answer lands promptly", () => {
    expect(PROVISIONAL_INVENTORY_POLL_MS).toBeLessThanOrEqual(PROVISIONAL_INVENTORY_TIMEOUT_MS / 4);
  });
});
