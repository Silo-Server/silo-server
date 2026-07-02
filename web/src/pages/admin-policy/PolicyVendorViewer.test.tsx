// @vitest-environment jsdom

import { cleanup, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@uiw/react-codemirror", () => ({
  default: ({ value }: { value: string }) => <textarea readOnly value={value} />,
}));

import { PolicyVendorViewer } from "./PolicyVendorViewer";
import {
  installPolicyStorageMocks,
  jsonResponse,
  renderWithPolicyProviders,
} from "./policyTestUtils";
import { parseRankLadder } from "./vendorBaseline";

const RATINGS_SOURCE = `package silo.lib.ratings

import rego.v1

rank := {
\t"G": 0,
\t"TV-Y": 0,
\t"PG": 1,
\t"R": 3,
}
`;

describe("PolicyVendorViewer", () => {
  beforeEach(() => {
    installPolicyStorageMocks();
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async () =>
        jsonResponse([
          { path: "vendor/scope.rego", source: "package silo.scope" },
          { path: "vendor/lib/ratings.rego", source: RATINGS_SOURCE },
        ]),
      ),
    );
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("renders baseline rule summaries with the source collapsed", async () => {
    renderWithPolicyProviders(<PolicyVendorViewer />);

    expect(await screen.findByText("Library visibility")).toBeInTheDocument();
    expect(screen.getByText(/hid for themselves stay hidden/)).toBeInTheDocument();
    // Rating tiers parsed from the module source, grouped by rank.
    expect(screen.getByText("G · TV-Y")).toBeInTheDocument();
    expect(screen.getByText("R")).toBeInTheDocument();
    // Source stays behind an accordion trigger rather than dumped inline.
    expect(screen.getAllByText(/View Rego source/).length).toBeGreaterThan(0);
  });
});

describe("parseRankLadder", () => {
  it("groups entries into ordered tiers and maps the empty label to Any", () => {
    const tiers = parseRankLadder('rank := {\n\t"": 0,\n\t"480P": 1,\n\t"720P": 2,\n}');
    expect(tiers).toEqual([
      { rank: 0, labels: ["Any"] },
      { rank: 1, labels: ["480P"] },
      { rank: 2, labels: ["720P"] },
    ]);
  });

  it("returns undefined when no rank table exists", () => {
    expect(parseRankLadder("package silo.lib.other\n\nallow := true")).toBeUndefined();
  });
});
