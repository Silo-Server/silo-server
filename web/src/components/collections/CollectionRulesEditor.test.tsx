import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";

import { createEmptyQueryDefinition } from "@/api/types";

import CollectionRulesEditor from "./CollectionRulesEditor";

vi.mock("@/components/FilterRuleEditor", () => ({
  default: ({ mediaScope }: { mediaScope?: string }) => (
    <div>filter rule editor mediaScope:{mediaScope}</div>
  ),
}));

describe("CollectionRulesEditor", () => {
  it("forwards ebook media scope to advanced rule labels", () => {
    const markup = renderToStaticMarkup(
      <CollectionRulesEditor
        value={{
          ...createEmptyQueryDefinition(),
          media_scope: "ebook",
        }}
        onChange={() => {}}
        allowPersonalizedFilters
      />,
    );

    expect(markup).toContain("mediaScope:ebook");
  });
});
