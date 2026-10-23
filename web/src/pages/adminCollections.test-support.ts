import type { components } from "@/api/v2/schema";
import { v2Recorder, type RecordedCall } from "@/test/v2Recorder";

type AdminCollection = components["schemas"]["AdminCollection"];

/** A server collection in Movies (library 1) whose id is its title in kebab case. */
export function storedAdminCollection(
  title: string,
  overrides: Partial<AdminCollection> = {},
): AdminCollection {
  return {
    id: title.toLowerCase().replace(/\W+/g, "-"),
    library_id: "1",
    library_ids: ["1"],
    slug: "",
    title,
    description: "",
    collection_type: "manual",
    visibility: "visible",
    sort_order: 0,
    group_id: null,
    featured: false,
    poster_url: "",
    poster_is_collage: false,
    backdrop_url: "",
    source_url: "",
    query_definition: {},
    sort_config: {},
    source_config: {},
    management_mode: "manual",
    management_source: "",
    management_key: "",
    last_sync_status: "",
    last_sync_message: "",
    item_count: 0,
    home_row_count: 0,
    row_count: 0,
    created_at: "2026-01-02T03:04:05.000Z",
    updated_at: "2026-01-02T03:04:05.000Z",
    ...overrides,
  };
}

export const ADMIN_COLLECTION_CAPABILITIES = {
  groups: true,
  imports: true,
  import_sources: [],
  artwork: true,
  item_reorder: true,
  section_references: true,
};

/**
 * Answers Admin Collections from `items()`, read on every request: the list,
 * each collection, and its delete and sync.
 */
export function answerAdminCollections(items: () => AdminCollection[]) {
  v2Recorder.answer("GET /api/v2/admin/collections", () => ({ items: items(), groups: [] }));
  v2Recorder.answer("GET /api/v2/admin/collections/capabilities", ADMIN_COLLECTION_CAPABILITIES);
  v2Recorder.answer("GET /api/v2/admin/collections/{id}", (call: RecordedCall) =>
    items().find((entry) => call.path.endsWith(`/${entry.id}`)),
  );
  v2Recorder.answer("DELETE /api/v2/admin/collections/{id}", undefined);
  v2Recorder.answer("POST /api/v2/admin/collections/{id}/sync", {
    status: "success",
    message: "",
    items_matched: 3,
  });
}
