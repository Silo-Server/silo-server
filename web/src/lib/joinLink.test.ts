import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { buildJoinLink, type JoinLinkForm } from "./joinLink";

type JoinLinkCase = {
  type: string;
  name: string;
  valid: boolean;
  form?: JoinLinkForm;
  string: string;
  address?: string;
  app_code?: string;
};

// The protocol's own vectors, as the server's tests read them: a link the
// app can't parse would strand a person on the Open Silo step.
const vectors = JSON.parse(
  readFileSync(
    fileURLToPath(
      new URL("../../../internal/appkeys/testdata/vectors/v1/links.json", import.meta.url),
    ),
    "utf8",
  ),
) as { cases: JoinLinkCase[] };

const cases = vectors.cases.filter((c) => c.type === "join-link" && c.valid);

describe("buildJoinLink", () => {
  it("has the protocol's join link cases", () => {
    expect(cases.length).toBeGreaterThan(0);
  });
  it.each(cases.map((c) => [c.name, c] as const))("builds %s", (_, c) => {
    expect(buildJoinLink(c.address!, c.app_code!, c.form!)).toBe(c.string);
  });
});
