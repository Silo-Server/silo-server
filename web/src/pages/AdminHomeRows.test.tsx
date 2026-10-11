import { describe, expect, it } from "vitest";

import { queryDefinitionFromSectionConfig, queryDefinitionToSectionConfig } from "@/api/types";

describe("AdminSections", () => {
  it("serializes section filters into the shared query definition shape", () => {
    const query = queryDefinitionFromSectionConfig({
      filter_type: "movie",
      filter_library_ids: [2],
      match: "all",
      groups: [{ match: "all", rules: [{ field: "genre", op: "is", value: "Action" }] }],
      sort: "rating",
      order: "desc",
    });

    expect(query.media_scope).toBe("movie");
    expect(query.library_ids).toEqual([2]);
    expect(queryDefinitionToSectionConfig(query)).toMatchObject({
      media_scope: "movie",
      library_ids: [2],
      match: "all",
    });
  });
});
