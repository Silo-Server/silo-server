import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import MetadataBadges from "./MetadataBadges";

describe("MetadataBadges advisory age", () => {
  it("attributes the age to the service that recommended it", () => {
    render(<MetadataBadges contentRating="PG" advisoryAge={13} advisorySource="commonsense" />);
    expect(screen.getByText("Common Sense 13+")).toBeTruthy();
    // The certification is a separate badge and must still render beside it.
    expect(screen.getByText("PG")).toBeTruthy();
  });
});
