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

  it("falls back to a bare age when the source is unknown to the UI", () => {
    render(<MetadataBadges advisoryAge={13} advisorySource="something-new" />);
    expect(screen.getByText("13+")).toBeTruthy();
  });

  it("renders nothing without an advisory age", () => {
    render(<MetadataBadges contentRating="PG" advisorySource="commonsense" />);
    expect(screen.queryByText(/\+$/)).toBeNull();
    expect(screen.queryByText(/Common Sense/)).toBeNull();
  });

  it("ignores a non-positive age rather than showing 0+", () => {
    render(<MetadataBadges advisoryAge={0} advisorySource="commonsense" />);
    expect(screen.queryByText(/Common Sense/)).toBeNull();
  });

  it("names who suggested the age in the tooltip", () => {
    render(<MetadataBadges advisoryAge={13} advisorySource="commonsense" />);
    const badge = screen.getByText("Common Sense 13+");
    expect(badge.getAttribute("title")).toBe("Common Sense suggests age 13 and up.");
  });
});
