/**
 * Data the Settings › Requests tests hand to their module mocks (plugin
 * installations, admin users). It imports nothing from the app, so a
 * `vi.mock` factory can load it without a cycle.
 */
import type { PluginAdminForm } from "@/api/types";

// The Sonarr/Radarr plugin's form, trimmed to the fields these tests touch.
const field = (extra: Record<string, unknown>) => ({
  required: false,
  secret: false,
  multiline: false,
  ...extra,
});
export const descriptor = {
  fields: [
    field({
      key: "service_kind",
      label: "Service",
      control: "SELECT",
      required: true,
      options: [
        { value: "radarr", label: "Radarr (movies)" },
        { value: "sonarr", label: "Sonarr (series)" },
      ],
    }),
    field({ key: "root_folder", label: "Root folder", control: "SELECT", dynamic_options: true }),
    field({
      key: "quality_profile_id",
      label: "Quality profile",
      control: "SELECT",
      required: true,
      dynamic_options: true,
    }),
    field({ key: "tags", label: "Tags", control: "MULTI_SELECT", dynamic_options: true }),
    field({ key: "is_default", label: "Default (HD/1080p)", control: "SWITCH" }),
    field({ key: "is_4k", label: "4K instance", control: "SWITCH" }),
    field({
      key: "is_default_4k",
      label: "Default 4K (2160p)",
      control: "SWITCH",
      show_when: [{ field: "is_4k", equals: ["true"] }],
    }),
    field({ key: "search_on_add", label: "Search on add", control: "SWITCH", default_value: true }),
    field({
      key: "series_type",
      label: "Series type",
      control: "SELECT",
      default_value: "standard",
      options: [
        { value: "standard", label: "Standard" },
        { value: "daily", label: "Daily" },
        { value: "anime", label: "Anime" },
      ],
      show_when: [{ field: "service_kind", equals: ["sonarr"] }],
    }),
    field({ key: "anime_enabled", label: "Enable anime overrides", control: "SWITCH" }),
    field({
      key: "anime_root_folder",
      label: "Anime root folder",
      control: "SELECT",
      dynamic_options: true,
      show_when: [{ field: "anime_enabled", equals: ["true"] }],
    }),
  ],
  sections: [
    {
      key: "library",
      title: "Library",
      collapsible: true,
      collapsed_default: true,
      field_keys: [
        "service_kind",
        "root_folder",
        "quality_profile_id",
        "tags",
        "is_default",
        "is_4k",
        "is_default_4k",
        "search_on_add",
        "series_type",
      ],
    },
    {
      key: "anime",
      title: "Anime overrides",
      collapsible: false,
      collapsed_default: false,
      field_keys: ["anime_enabled", "anime_root_folder"],
    },
  ],
} as PluginAdminForm;
export const jsonSchema = JSON.stringify({
  type: "object",
  properties: {
    service_kind: { type: "string" },
    root_folder: { type: "string" },
    quality_profile_id: { type: "integer" },
    tags: { type: "array", items: { type: "integer" } },
    is_default: { type: "boolean" },
    is_4k: { type: "boolean" },
    is_default_4k: { type: "boolean" },
    search_on_add: { type: "boolean" },
    series_type: { type: "string" },
    anime_enabled: { type: "boolean" },
    anime_root_folder: { type: "string" },
  },
});

/** What `useAdminPluginInstallations` answers: the arr plugin, installed once. */
export const pluginInstallations = {
  data: [
    {
      id: 1,
      plugin_id: "silo.requests.arr",
      enabled: true,
      capabilities: [
        {
          type: "request_router.v1",
          id: "arr",
          display_name: "Sonarr / Radarr",
          config_schema: [
            {
              key: "connection",
              title: "Connection",
              json_schema: jsonSchema,
              required: false,
              admin_form: descriptor,
            },
          ],
        },
      ],
    },
  ],
  isLoading: false,
};

export const adminUsers = {
  data: [
    { id: 1, username: "admin" },
    { id: 2, username: "kid" },
  ],
  isLoading: false,
};
