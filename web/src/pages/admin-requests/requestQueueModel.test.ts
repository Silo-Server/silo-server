// @vitest-environment node

import { describe, expect, it } from "vitest";
import type { MediaRequest } from "@/api/types";
import {
  defaultRequestQueueView,
  distinctTargets,
  parseRequestQueueUser,
  parseRequestQueueView,
  requestQueueActions,
  requestQueueView,
  requestOptionColumns,
  requestOptionValue,
} from "./requestQueueModel";

type Fields = Pick<MediaRequest, "status" | "outcome" | "targets">;
const sent = [{ quality: "1080p", status: "queued" }] as MediaRequest["targets"];

describe("requestQueueView", () => {
  it.each<[string, Fields, string]>([
    ["pending", { status: "pending", outcome: "active" }, "needs_approval"],
    ["approved", { status: "approved", outcome: "active" }, "in_progress"],
    ["queued", { status: "queued", outcome: "active" }, "in_progress"],
    ["downloading", { status: "downloading", outcome: "active" }, "in_progress"],
    ["completed", { status: "completed", outcome: "active" }, "done"],
    ["failed", { status: "approved", outcome: "failed" }, "failed"],
    ["declined while pending", { status: "pending", outcome: "declined" }, "done"],
    ["cancelled", { status: "approved", outcome: "cancelled" }, "done"],
  ])("puts a %s request where the server does", (_label, request, view) => {
    expect(requestQueueView(request)).toBe(view);
  });
});

describe("requestQueueActions", () => {
  it.each<[string, Fields, string[]]>([
    ["pending", { status: "pending", outcome: "active" }, ["approve", "decline"]],
    ["approved, nothing sent", { status: "approved", outcome: "active", targets: [] }, ["cancel"]],
    ["approved and sent", { status: "approved", outcome: "active", targets: sent }, []],
    ["queued", { status: "queued", outcome: "active", targets: sent }, []],
    // Admin cancel accepts only an active request, so a failed one offers Retry alone.
    ["failed", { status: "approved", outcome: "failed", targets: [] }, ["retry", "cancel"]],
    ["completed", { status: "completed", outcome: "active" }, []],
    ["declined", { status: "pending", outcome: "declined" }, []],
  ])("offers a %s request only what the server allows", (_label, request, actions) => {
    expect(requestQueueActions(request)).toEqual(actions);
  });
});

describe("queue links", () => {
  it("opens on what needs approval, else on what is under way", () => {
    const counts = { needs_approval: 0, in_progress: 0, failed: 2, done: 9 };
    expect(defaultRequestQueueView(counts)).toBe("in_progress");
    expect(defaultRequestQueueView({ ...counts, needs_approval: 1 })).toBe("needs_approval");
  });

  it("reads only known views and positive account IDs", () => {
    expect(parseRequestQueueView("failed")).toBe("failed");
    expect(parseRequestQueueView("settings")).toBeUndefined();
    expect(parseRequestQueueView(null)).toBeUndefined();
    expect(parseRequestQueueUser("7")).toBe(7);
    for (const value of ["0", "-1", "7a", "01", "", null, "99999999999"]) {
      expect(parseRequestQueueUser(value)).toBeUndefined();
    }
  });
});

describe("distinctTargets", () => {
  const target = (id: number, quality: string, external_id?: string, integration_id = "seerr") =>
    ({ id, quality, external_id, integration_id, status: "queued" }) as NonNullable<
      MediaRequest["targets"]
    >[number];

  it("shows one download that serves both qualities once", () => {
    const entries = distinctTargets([target(1, "1080p", "42"), target(2, "2160p", "42")]);
    expect(entries.map((entry) => [entry.target.id, entry.shared])).toEqual([[1, true]]);
  });

  it("keeps separate downloads, and targets without an external id, apart", () => {
    const entries = distinctTargets([
      target(1, "1080p", "42"),
      target(2, "2160p", "43"),
      target(3, "1080p", "42", "other"),
      target(4, "1080p"),
      target(5, "2160p"),
    ]);
    expect(entries.map((entry) => entry.target.id)).toEqual([1, 2, 3, 4, 5]);
    expect(entries.every((entry) => !entry.shared)).toBe(true);
  });
});

describe("requestOptionColumns", () => {
  const movie = {
    options: [
      { key: "radarr_profile_id", label: "Quality", value: "4", value_label: "Ultra-HD" },
      { key: "radarr_root_folder", label: "Folder", value: "/m", value_label: "/movies" },
    ],
  };
  const series = {
    options: [
      { key: "sonarr_profile_id", label: "Quality", value: "9", value_label: "Series UHD" },
    ],
  };

  it("names each chosen setting once, and reads a request's value for it", () => {
    expect(requestOptionColumns([{}, series, movie])).toEqual(["Quality", "Folder"]);
    expect(requestOptionColumns([{}, { options: [] }])).toEqual([]);
    expect(requestOptionValue(series, "Quality")).toBe("Series UHD");
    expect(requestOptionValue(series, "Folder")).toBeUndefined();
  });
});
