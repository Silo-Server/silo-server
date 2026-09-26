import type { ProfileRequestContextSnapshot } from "@/api/client";
import type { AutoscanAvailableSource } from "@/api/types";
import { v2 } from "./request";
import { readAutoscanPages } from "./adminAutoscanPagination";

export async function readAdminAutoscanAvailableSources(
  profileContext: ProfileRequestContextSnapshot,
): Promise<AutoscanAvailableSource[]> {
  return readAutoscanPages(
    profileContext,
    (cursor) =>
      v2("GET /api/v2/admin/autoscan/scan-source-plugins", {
        profileContext,
        query: { limit: 100, cursor },
      }),
    (items) =>
      items.map(
        (row): AutoscanAvailableSource => ({
          ...row,
          descriptor: {
            ...row.descriptor,
            delivery_modes: row.descriptor.delivery_modes.filter(
              (mode): mode is "poll" | "webhook" => mode === "poll" || mode === "webhook",
            ),
            connection:
              row.descriptor.connection === "none" || row.descriptor.connection === "required"
                ? row.descriptor.connection
                : "optional",
            config_form: row.descriptor.config_form
              ? {
                  ...row.descriptor.config_form,
                  fields: row.descriptor.config_form.fields.map((field) => ({
                    ...field,
                    control:
                      field.control === "TEXTAREA" ||
                      field.control === "PASSWORD" ||
                      field.control === "NUMBER" ||
                      field.control === "SWITCH" ||
                      field.control === "SELECT" ||
                      field.control === "MULTI_SELECT"
                        ? field.control
                        : "TEXT",
                    required: field.required ?? false,
                    secret: field.secret ?? false,
                    multiline: field.multiline ?? false,
                  })),
                  sections: row.descriptor.config_form.sections?.map((section) => ({
                    ...section,
                    collapsible: section.collapsible ?? false,
                    collapsed_default: section.collapsed_default ?? false,
                  })),
                }
              : undefined,
          },
        }),
      ),
    "Invalid autoscan source descriptor page.",
    "Invalid autoscan source descriptor continuation.",
    "Too many autoscan source descriptors to display.",
  );
}
