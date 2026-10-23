import type { PluginAdminFormField } from "@/api/types";

/** Builds an admin form field that is optional, not secret, and single-line unless `extra` says otherwise. */
export function field(
  key: string,
  label: string,
  control: PluginAdminFormField["control"],
  extra: Partial<PluginAdminFormField> = {},
): PluginAdminFormField {
  return { key, label, control, required: false, secret: false, multiline: false, ...extra };
}
