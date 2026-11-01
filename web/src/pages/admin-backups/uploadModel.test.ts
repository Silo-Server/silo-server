// @vitest-environment node

import { describe, expect, it } from "vitest";

import type { AdminBackupUpload } from "@/api/v2/adminBackupUpload";
import { V2ProblemError } from "@/api/v2/request";

import {
  failureCopy,
  failureOfError,
  PartFailedError,
  partRange,
  sendParts,
  sniffRestoreSet,
} from "./uploadModel";

function problem(type: string, status: number) {
  return new V2ProblemError("op", {
    type: `https://siloserver.org/docs/api/v2/problems/${type}`,
    title: type,
    status,
    detail: `detail ${type}`,
    instance: "urn:silo:request:1",
  });
}

function session(size: number, partSize: number, received = 0): AdminBackupUpload {
  return {
    id: "u1",
    status: "uploading",
    size_bytes: size,
    part_size: partSize,
    part_count: Math.ceil(size / partSize),
    received_parts: 0,
    received_bytes: received,
    created_at: "2026-10-31T04:00:00Z",
    updated_at: "2026-10-31T04:00:00Z",
    location: "",
  };
}

describe("partRange", () => {
  it("cuts every part but the last to the part size", () => {
    expect(partRange(0, 4, 10)).toEqual([0, 4]);
    expect(partRange(2, 4, 10)).toEqual([8, 10]);
  });
});

describe("sniffRestoreSet", () => {
  const tarHead = (name: string) => {
    const head = new Uint8Array(512);
    head.set(new TextEncoder().encode(name), 0);
    head.set(new TextEncoder().encode("ustar\u000000"), 257);
    return head;
  };

  it("accepts a tar whose first entry is restore-set.json, behind a PAX header or not", () => {
    expect(sniffRestoreSet(tarHead("restore-set.json"))).toBe("restore_set");
    expect(sniffRestoreSet(tarHead("PaxHeaders.0/restore-set.json"))).toBe("restore_set");
  });

  it("refuses anything else", () => {
    const text = (value: string) => new TextEncoder().encode(value.padEnd(600, "x"));
    expect(sniffRestoreSet(tarHead("db.dump"))).toBe("not_a_restore_set");
    expect(sniffRestoreSet(text("PGDMP"))).toBe("not_a_restore_set");
  });
});

describe("failures", () => {
  const limits = { sizeBytes: 3, limitBytes: 2 };

  it("waits out a conflict only once the file arrived", () => {
    expect(failureOfError(problem("conflict", 409), "complete", limits)).toEqual({ kind: "busy" });
    expect(failureOfError(problem("conflict", 409), "part", limits).kind).toBe("error");
  });

  it("offers to continue the same upload only when its parts are kept", () => {
    expect(failureCopy({ kind: "part_failed", part: 2, partCount: 16 }).resumable).toBe(true);
    expect(failureCopy({ kind: "busy" }).resumable).toBe(true);
    expect(
      failureCopy({ kind: "rejected", reason: "not_a_restore_set", message: "" }).resumable,
    ).toBe(false);
  });
});

describe("sendParts", () => {
  const file = new Blob([new Uint8Array(10)]);

  it("sends the parts from where it stopped, retrying a part that failed in transit", async () => {
    const sent: string[] = [];
    let flaky = 1;
    const waits: number[] = [];
    const result = await sendParts({
      upload: session(10, 4),
      file,
      from: 1,
      wait: async (ms) => {
        waits.push(ms);
      },
      put: async (part, bytes) => {
        if (part === 1 && flaky-- > 0) throw new TypeError("network");
        sent.push(`${part}:${bytes.size}`);
        return session(10, 4, Math.min(10, (part + 1) * 4));
      },
    });
    expect(sent).toEqual(["1:4", "2:2"]);
    expect(waits).toEqual([1_000]);
    expect(result.received_bytes).toBe(10);
  });

  it("names the part that kept failing, and never retries a refusal", async () => {
    let calls = 0;
    const failing = (error: unknown) =>
      sendParts({
        upload: session(10, 4),
        file,
        from: 0,
        attempts: 3,
        wait: async () => undefined,
        put: async () => {
          calls += 1;
          throw error;
        },
      });
    await expect(failing(problem("internal_error", 500))).rejects.toMatchObject({ part: 0 });
    expect(calls).toBe(3);
    // An earlier send of the part still being received is waited out.
    calls = 0;
    await expect(failing(problem("conflict", 409))).rejects.toMatchObject({ part: 0 });
    expect(calls).toBe(3);
    calls = 0;
    const refused = failing(problem("validation_failed", 422));
    await expect(refused).rejects.toBeInstanceOf(PartFailedError);
    expect(calls).toBe(1);
  });
});
