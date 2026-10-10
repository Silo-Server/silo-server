import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { PolicyLimitFields, policyStateFromUser } from "./UserPolicyFields";

describe("PolicyLimitFields", () => {
  it("lets inherited policy selects grow to fit their label instead of truncating it", () => {
    const html = renderToStaticMarkup(
      <PolicyLimitFields
        state={policyStateFromUser(null)}
        onChange={() => {}}
        source="group"
        effective={{ transcode_allowed: false }}
      />,
    );

    expect(html).toContain("min-w-40");
    expect(html).not.toMatch(/class="(?:[^"]*\s)?w-40[\s"]/);
  });

  it("caps inherited policy selects at the row width and wraps the label inside it", () => {
    const html = renderToStaticMarkup(
      <PolicyLimitFields
        state={policyStateFromUser(null)}
        onChange={() => {}}
        source="server"
        effective={{ transcode_allowed: false }}
      />,
    );

    const triggers = [...html.matchAll(/class="([^"]*\bmin-w-40\b[^"]*)"/g)].map((m) => m[1] ?? "");
    expect(triggers).toHaveLength(2);
    for (const classes of triggers) {
      expect(classes.split(" ")).toEqual(
        expect.arrayContaining(["max-w-full", "whitespace-normal", "data-[size=default]:h-auto"]),
      );
      expect(classes).not.toContain("whitespace-nowrap");
      expect(classes).not.toContain("data-[size=default]:h-9");
    }
  });
});
