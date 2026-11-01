// @vitest-environment node

import { describe, expect, it } from "vitest";

import type { AdminBackup, AdminBackupList } from "@/api/v2/adminBackups";

import {
  canRestoreParts,
  keyConfirmationNeeded,
  restoreCommand,
  retentionPlan,
} from "./backupPresentation";

function backup(day: number, overrides: Partial<AdminBackup> = {}): AdminBackup {
  return {
    id: `${day}`,
    kind: "nightly",
    status: "complete",
    created_at: new Date(2026, 9, day, 4).toISOString(),
    size_bytes: 1,
    blob_count: 0,
    location: `/data/backups/srv-1/${day}`,
    missing_blobs: [],
    objects: [],
    ...overrides,
  } as AdminBackup;
}

/** Nightly copies for the given days of the month, newest first as the list returns them. */
function nightlies(...days: number[]) {
  return days.sort((a, b) => b - a).map((day) => backup(day));
}

describe("retentionPlan", () => {
  it.each([
    ["deletes the oldest when the strip is full", nightlies(25, 26, 27, 28, 29, 30, 31), 7, ["25"]],
    [
      "keeps the newest copy without warnings behind newer copies with them",
      [
        backup(31, { status: "complete_with_warnings" }),
        backup(30, { status: "complete_with_warnings" }),
        backup(29),
        backup(28),
      ],
      2,
      ["28", "30"],
      "29",
    ],
    [
      "deletes past the limit when every copy has warnings",
      [
        backup(31, { status: "complete_with_warnings" }),
        backup(30, { status: "complete_with_warnings" }),
      ],
      1,
      ["30", "31"],
      null,
    ],
  ])("%s", (_case, items, keep, doomed, protectedId = null) => {
    const plan = retentionPlan(items, keep);
    expect([...plan.doomed].sort()).toEqual(doomed);
    expect(plan.protectedId).toBe(protectedId);
  });

  it("counts manual copies with nightly ones and ignores the rest", () => {
    const plan = retentionPlan(
      [
        backup(31, { id: "running", status: "in_progress" }),
        backup(30, { id: "manual", kind: "manual", status: "complete_with_warnings" }),
        backup(29, { id: "failed", status: "incomplete" }),
        backup(28, { id: "restore", kind: "before-restore" }),
        backup(26, { id: "nightly" }),
      ],
      2,
    );
    expect(plan.kept.map((item) => item.id)).toEqual(["nightly", "manual"]);
    expect([...plan.doomed]).toEqual([]);
    expect(plan.protectedId).toBe("nightly");
  });
});

describe("restoreCommand", () => {
  it.each([
    [
      "quotes a folder a shell would split",
      "/srv/silo data/it's/backups/srv-1/x",
      "silo backup restore --from '/srv/silo data/it'\\''s/backups/srv-1/x'",
    ],
    [
      "reads a backup in a bucket from its address, without the trailing slash",
      "s3://backups/silo/backups/srv-1/20261031T040000Z-nightly/",
      "silo backup restore --from s3://backups/silo/backups/srv-1/20261031T040000Z-nightly",
    ],
  ])("%s", (_case, location, expected) => {
    expect(restoreCommand(location)).toBe(expected);
  });
});

describe("canRestoreParts", () => {
  it.each([
    ["a plain restore set", ["db.dump", "restore-set.tar", "manifest.json"], true],
    ["an encrypted restore set", ["db.dump.age", "restore-set.tar.age", "manifest.json"], true],
    ["no restore set", ["db.dump", "blobs.tar", "manifest.json"], false],
  ])("%s", (_case, names, expected) => {
    const objects = names.map((name) => ({ name, sha256: "", size_bytes: 1 }));
    expect(canRestoreParts(backup(31, { objects }))).toBe(expected);
  });
});

describe("keyConfirmationNeeded", () => {
  const list = (overrides: Partial<AdminBackupList>) =>
    ({ destination: "s3", encrypted: true, key_confirmed: false, ...overrides }) as AdminBackupList;

  it.each([
    [
      "asks on the default destination when it is an encrypted private bucket",
      list({ destination: "default" }),
      true,
    ],
    [
      "not on the default destination on this server's disk",
      list({ destination: "default", encrypted: false }),
      false,
    ],
    ["not for a confirmed key", list({ key_confirmed: true }), false],
  ])("%s", (_case, value, expected) => {
    expect(keyConfirmationNeeded(value)).toBe(expected);
  });
});
