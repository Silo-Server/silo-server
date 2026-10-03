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
});
