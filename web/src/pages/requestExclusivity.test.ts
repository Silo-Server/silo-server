import { describe, it, expect } from "vitest";
import type { PluginAdminFormField } from "@/api/types";
import { applyExclusivity, type ExclusivityCard } from "./requestExclusivity";

const isDefault: PluginAdminFormField = {
  key: "is_default",
  label: "HD default",
  control: "SWITCH",
  required: false,
  secret: false,
  multiline: false,
  exclusive_group_field: "service_kind",
};
const fieldsFor = () => [isDefault];

describe("applyExclusivity", () => {
  it("clears the default on a same-group sibling when a card turns it on", () => {
    const cards: ExclusivityCard[] = [
      { key: "a", installationId: "5", config: { service_kind: "radarr", is_default: true } },
      { key: "b", installationId: "5", config: { service_kind: "radarr", is_default: false } },
    ];
    const out = applyExclusivity(
      cards,
      "b",
      { service_kind: "radarr", is_default: true },
      fieldsFor,
    );
    expect(out.find((c) => c.key === "a")!.config.is_default).toBe(false);
    expect(out.find((c) => c.key === "b")!.config.is_default).toBe(true);
  });

  it("leaves a different-group sibling untouched", () => {
    const cards: ExclusivityCard[] = [
      { key: "a", installationId: "5", config: { service_kind: "sonarr", is_default: true } },
      { key: "b", installationId: "5", config: { service_kind: "radarr", is_default: false } },
    ];
    const out = applyExclusivity(
      cards,
      "b",
      { service_kind: "radarr", is_default: true },
      fieldsFor,
    );
    expect(out.find((c) => c.key === "a")!.config.is_default).toBe(true);
  });

  it("no-ops when the changed field is turned off", () => {
    const cards: ExclusivityCard[] = [
      { key: "a", installationId: "5", config: { service_kind: "radarr", is_default: true } },
      { key: "b", installationId: "5", config: { service_kind: "radarr", is_default: false } },
    ];
    const out = applyExclusivity(
      cards,
      "b",
      { service_kind: "radarr", is_default: false },
      fieldsFor,
    );
    expect(out.find((c) => c.key === "a")!.config.is_default).toBe(true);
  });
});
