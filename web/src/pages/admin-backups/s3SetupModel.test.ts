// @vitest-environment node

import { describe, expect, it } from "vitest";

import {
  BACKUP_S3_KEYS,
  s3SaveBlocker,
  s3SettingsValues,
  s3TestBody,
  type S3Fields,
  type S3TestOutcome,
} from "./s3SetupModel";

const FIELDS: S3Fields = {
  endpoint: "https://s3.example.com",
  region: "us-east-1",
  bucket: "silo-backups",
  keyPrefix: "home",
  accessKey: "AKIA",
  secretKey: "",
  pathStyle: false,
};

const passed = (fields: S3Fields, ok = true): S3TestOutcome => ({
  body: s3TestBody(fields),
  result: {
    ok,
    message: ok ? "Connected. Silo wrote and deleted a test file." : "No such bucket.",
  },
});

describe("s3SaveBlocker", () => {
  it.each([
    ["saves after a passed test and the ticked box", FIELDS, passed(FIELDS), true, false],
    ["needs a test", FIELDS, null, true, true],
    ["needs the test to pass", FIELDS, passed(FIELDS, false), true, true],
    [
      "needs a new test after a field changed",
      { ...FIELDS, keyPrefix: "other" },
      passed(FIELDS),
      true,
      true,
    ],
    [
      "needs a new test after a secret was typed",
      { ...FIELDS, secretKey: "s3cret" },
      passed(FIELDS),
      true,
      true,
    ],
    ["needs the box ticked", FIELDS, passed(FIELDS), false, true],
  ])("%s", (_case, fields, outcome, keyKept, blocked) => {
    expect(s3SaveBlocker({ fields, outcome, keyKept }) !== null).toBe(blocked);
  });
});

describe("s3SettingsValues", () => {
  it("switches to the bucket and keeps the stored secret when none was typed", () => {
    const values = s3SettingsValues(FIELDS);
    expect(values["backup.destination"]).toBe("s3");
    expect(values).not.toHaveProperty(BACKUP_S3_KEYS.secretKey);
    expect(values).not.toHaveProperty("backup.s3.key_acknowledged");
  });

  it("sends a typed secret", () => {
    const fields = { ...FIELDS, secretKey: "s3cret" };
    expect(s3SettingsValues(fields)[BACKUP_S3_KEYS.secretKey]).toBe("s3cret");
  });
});
