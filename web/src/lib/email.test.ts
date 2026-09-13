import { describe, expect, it } from "vitest";

import { isValidEmail } from "./email";

describe("isValidEmail", () => {
  it.each([
    "admin@example.com",
    "  admin@example.com ",
    "first.last+tag@sub.example.co.uk",
    "admin@[192.0.2.1]",
  ])("accepts %s", (value) => {
    expect(isValidEmail(value)).toBe(true);
  });

  it.each([
    "admin@siloserver",
    "admin@",
    "@example.com",
    "admin@example.",
    "admin@.example",
    "",
    "   ",
    "not an email",
    "Admin <admin@example.com>",
    "a@b@example.com",
  ])("rejects %s", (value) => {
    expect(isValidEmail(value)).toBe(false);
  });
});
