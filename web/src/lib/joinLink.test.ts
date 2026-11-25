import { describe, expect, it } from "vitest";
import { buildJoinLink, type JoinLinkForm } from "./joinLink";
import vectors from "./joinLink.vectors.json";

type JoinLinkCase = {
  name: string;
  form: JoinLinkForm;
  string: string;
  address: string;
  app_code: string;
};

// The protocol's own join link vectors (a copy the server's tests keep equal
// to its vector file): a link the app can't parse would strand a person on
// the Open Silo step.
const cases = vectors.cases as JoinLinkCase[];

describe("buildJoinLink", () => {
  it("has the protocol's join link cases", () => {
    expect(cases.length).toBeGreaterThan(0);
  });
  it.each(cases.map((c) => [c.name, c] as const))("builds %s", (_, c) => {
    expect(buildJoinLink(c.address, c.app_code, c.form)).toBe(c.string);
  });
});
