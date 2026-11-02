// @vitest-environment node

import { describe, expect, it } from "vitest";

import type { AdminBackupOmitted } from "@/api/v2/adminBackups";

import { groupOmissions } from "./warningDetails";

const NOTIFY_ERROR = (profile: string) =>
  `Its data could not be read: profile ${profile} notifications: reading notification preferences: ERROR: column "notify_favorites" does not exist (SQLSTATE 42703)`;

function omitted(
  label: string,
  message: string,
  reason: AdminBackupOmitted["reason"] = "export_failed",
): AdminBackupOmitted {
  return { section: "users", id: label, label, reason, message };
}

describe("groupOmissions", () => {
  it("collapses one cause across accounts and keeps reasons apart, largest first", () => {
    const groups = groupOmissions([
      ...Array.from({ length: 182 }, (_, index) =>
        omitted(
          `user${index}`,
          NOTIFY_ERROR(`00000000-0000-4000-8000-${String(index).padStart(12, "0")}`),
        ),
      ),
      omitted(
        "big",
        "Its data is 900 bytes, more than a restore set document may hold (800).",
        "too_large",
      ),
      omitted(
        "bigger",
        "Its data is 950 bytes, more than a restore set document may hold (800).",
        "too_large",
      ),
    ]);
    expect(groups.map((group) => [group.reason, group.labels.length])).toEqual([
      ["export_failed", 182],
      ["too_large", 2],
    ]);
    expect(groups[0]!.detail).toBe('column "notify_favorites" does not exist (SQLSTATE 42703)');
  });

  it("groups by a structured cause and error instead of parsing the message", () => {
    const structured = (label: string, error: string): AdminBackupOmitted => ({
      ...omitted(label, `Its data could not be read: ${label} differs in every message`),
      cause: "notifications: reading notification preferences",
      error,
    });
    const groups = groupOmissions([
      structured("a", 'column "notify_favorites" does not exist (SQLSTATE 42703)'),
      structured("b", 'column "notify_favorites" does not exist (SQLSTATE 42703)'),
      structured("c", "deadlock detected (SQLSTATE 40P01)"),
    ]);
    expect(groups.map((group) => [group.labels.length, group.detail])).toEqual([
      [2, 'column "notify_favorites" does not exist (SQLSTATE 42703)'],
      [1, "deadlock detected (SQLSTATE 40P01)"],
    ]);
  });
});
