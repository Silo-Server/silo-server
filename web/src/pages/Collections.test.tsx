import { describe, expect, it } from "vitest";

import {
  buildUserCollectionCatalogHref,
  buildUserCollectionEditorPath,
  isCollectionReadOnly,
  toCreateCollectionBody,
  toUpdateCollectionBody,
  toUserCollectionBuilderValue,
} from "./userCollectionsShared";

describe("Collections helpers", () => {
  it("marks non-creator collections as read only", () => {
    expect(
      isCollectionReadOnly(
        {
          id: "col-1",
          profile_id: "profile-2",
          creator_profile_id: "profile-2",
          name: "Shared Picks",
          collection_type: "smart",
          is_shared: true,
          allowed_profile_ids: ["profile-1"],
          query_definition: {
            library_ids: [],
            match: "all",
            groups: [],
            sort: { field: "added_at", order: "desc" },
          },
          sort_config: {},
          sort_order: 0,
          group_id: null,
          created_at: "",
          updated_at: "",
        },
        "profile-1",
      ),
    ).toBe(true);
  });

  it("serializes smart collection access settings into the request body", () => {
    const builder = toUserCollectionBuilderValue(null);
    builder.title = "Action Night";
    builder.access = { is_shared: true, allowed_profile_ids: ["profile-1"] };

    expect(toCreateCollectionBody(builder)).toMatchObject({
      name: "Action Night",
      is_shared: true,
      allowed_profile_ids: ["profile-1"],
    });
  });

  it("sends a canonical display_query_definition fragment for manual collections", () => {
    const builder = toUserCollectionBuilderValue(null);
    builder.title = "Unwatched Movies";
    builder.collection_type = "manual";
    builder.display_query_definition = {
      match: "all",
      groups: [
        {
          match: "all",
          rules: [
            { field: "watched", op: "is", value: false },
            { field: "type", op: "is", value: "movie" },
          ],
        },
      ],
    };

    const createBody = toCreateCollectionBody(builder);
    expect(createBody.display_query_definition).toEqual({
      match: "all",
      groups: [
        {
          match: "all",
          rules: [
            { field: "watched", op: "is", value: false },
            { field: "type", op: "is", value: "movie" },
          ],
        },
      ],
    });
    expect(createBody).not.toHaveProperty("watch_filter");
    expect(createBody).not.toHaveProperty("media_filter");

    const updateBody = toUpdateCollectionBody(builder);
    expect(updateBody.display_query_definition).toEqual(createBody.display_query_definition);
    expect(updateBody).not.toHaveProperty("watch_filter");
    expect(updateBody).not.toHaveProperty("media_filter");
  });

  it("omits display_query_definition for manual collections with no display filter", () => {
    const builder = toUserCollectionBuilderValue(null);
    builder.collection_type = "manual";
    builder.display_query_definition = undefined;

    const createBody = toCreateCollectionBody(builder);
    expect(createBody.display_query_definition).toBeUndefined();
    expect(createBody).not.toHaveProperty("watch_filter");
  });

  it("builds the create route for user collections", () => {
    expect(buildUserCollectionEditorPath("new")).toBe("/collections/new");
  });

  it("builds the edit route for an existing user collection", () => {
    expect(buildUserCollectionEditorPath("col-3")).toBe("/collections/col-3/edit");
  });

  it("builds the catalog route for viewing a user collection", () => {
    expect(buildUserCollectionCatalogHref("col-3", "Shared Picks")).toBe(
      "/catalog?source=user_collection&collection_id=col-3&title=Shared+Picks",
    );
  });
});
