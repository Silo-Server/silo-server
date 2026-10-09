import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";

import { createEmptyQueryDefinition, type QueryDefinition } from "@/api/types";

import CollectionRulesEditor from "./CollectionRulesEditor";

vi.mock("@/components/FilterRuleEditor", () => ({
  default: ({ mediaScope }: { mediaScope?: string }) => (
    <div>filter rule editor mediaScope:{mediaScope}</div>
  ),
}));

beforeAll(() => {
  HTMLElement.prototype.scrollIntoView = vi.fn();
});

afterEach(cleanup);

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

  it.each<[NonNullable<QueryDefinition["media_scope"]>, string]>([
    ["video", "Movies & Series"],
    ["manga", "Manga"],
  ])("shows the %s media scope", (scope, label) => {
    render(
      <CollectionRulesEditor
        value={{ ...createEmptyQueryDefinition(), media_scope: scope }}
        onChange={() => {}}
        allowLibrarySelection={false}
      />,
    );

    expect(screen.getByRole("combobox")).toHaveTextContent(label);
  });

  it("saves the Movies & Series scope", () => {
    const onChange = vi.fn();
    render(
      <CollectionRulesEditor
        value={createEmptyQueryDefinition()}
        onChange={onChange}
        allowLibrarySelection={false}
      />,
    );

    fireEvent.click(screen.getByRole("combobox"));
    fireEvent.click(screen.getByRole("option", { name: "Movies & Series" }));

    expect(onChange).toHaveBeenCalledWith(
      expect.objectContaining({
        media_scope: "video",
        sort: { field: "added_at", order: "desc" },
      }),
    );
  });
});
