import { describe, expect, it } from "vitest";

import { createEmptyQueryDefinition, type QueryDefinition, type QueryGroup } from "@/api/types";

import {
  guidedStateToQueryDefinition,
  queryDefinitionToGuidedState,
} from "./CollectionGuidedRulesEditor";
import { isGuidedRepresentable } from "./guidedRepresentable";

describe("isGuidedRepresentable", () => {
  function definition(
    groups: QueryGroup[],
    overrides: Partial<QueryDefinition> = {},
  ): QueryDefinition {
    return { ...createEmptyQueryDefinition(), groups, ...overrides };
  }

  it.each<[string, QueryDefinition]>([
    ["an empty definition", createEmptyQueryDefinition()],
    [
      "a definition the Guided editor built",
      guidedStateToQueryDefinition(
        {
          ...queryDefinitionToGuidedState(createEmptyQueryDefinition()),
          mediaScope: "movie",
          libraryIds: [3],
          genres: ["Drama", "Sci-Fi"],
          yearFrom: "2015",
          actor: "Carrie-Anne Moss",
          originalLanguages: ["en", "fr"],
          watchStatus: "unwatched",
          fourK: true,
        },
        { ...createEmptyQueryDefinition(), limit: 250 },
      ),
    ],
    [
      "Guided rules in another order",
      definition([
        {
          match: "all",
          rules: [
            { field: "year", op: "gte", value: 2000 },
            { field: "genre", op: "is", value: "Drama" },
          ],
        },
      ]),
    ],
    [
      "a genre link from a book page",
      definition([{ match: "all", rules: [{ field: "genre", op: "contains", value: "Fantasy" }] }]),
    ],
  ])("is true for %s", (_label, query) => {
    expect(isGuidedRepresentable(query)).toBe(true);
  });

  it.each<[string, QueryDefinition]>([
    [
      "a negated rule",
      definition([{ match: "all", rules: [{ field: "genre", op: "is_not", value: "Horror" }] }]),
    ],
    [
      "an OR group",
      definition([
        {
          match: "any",
          rules: [
            { field: "genre", op: "is", value: "Comedy" },
            { field: "genre", op: "is", value: "Drama" },
          ],
        },
      ]),
    ],
    [
      "repeated actor rules",
      definition([
        {
          match: "all",
          rules: [
            { field: "actor", op: "is", value: "Actor A" },
            { field: "actor", op: "is", value: "Actor B" },
          ],
        },
      ]),
    ],
    [
      "groups joined with any",
      definition(
        [
          { match: "all", rules: [{ field: "genre", op: "is", value: "Comedy" }] },
          { match: "all", rules: [{ field: "year", op: "gte", value: 2000 }] },
        ],
        { match: "any" },
      ),
    ],
    [
      "a field Guided has no control for",
      definition([{ match: "all", rules: [{ field: "in_watchlist", op: "is", value: true }] }]),
    ],
    [
      "a narrator rule outside the audiobook scope",
      definition([{ match: "all", rules: [{ field: "narrator", op: "is", value: "Kramer" }] }]),
    ],
    [
      "half of the unwatched pair",
      definition([{ match: "all", rules: [{ field: "watched", op: "is", value: false }] }]),
    ],
  ])("is false for %s", (_label, query) => {
    expect(isGuidedRepresentable(query)).toBe(false);
  });
});
