import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";

import { createEmptyQueryDefinition } from "@/api/types";

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
