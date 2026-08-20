import type { PluginConfigSchema } from "@/api/types";
import { adminFormForConfigSchema } from "@/components/admin/plugins/configSchemaAdminForm";
import { buildSchemaValues, parseFieldTypes } from "@/components/admin/plugins/schemaFormUtils";
import type { WatchProviderConnectionConfig } from "@/hooks/queries/watchProviders";

export type RenderableConnectionSchema = PluginConfigSchema & {
  admin_form: NonNullable<PluginConfigSchema["admin_form"]>;
};

export function renderableConnectionSchemas(
  schemas: PluginConfigSchema[],
): RenderableConnectionSchema[] {
  return schemas.flatMap((schema) => {
    const adminForm = adminFormForConfigSchema(schema);
    return adminForm == null ? [] : [{ ...schema, admin_form: adminForm }];
  });
}

function hasEnteredValue(value: unknown): boolean {
  if (value == null) return false;
  if (typeof value === "string") return value.trim().length > 0;
  if (Array.isArray(value)) return value.some(hasEnteredValue);
  if (typeof value === "object") return Object.values(value).some(hasEnteredValue);
  return true;
}

export function activeConnectionSchemas(
  schemas: PluginConfigSchema[],
  drafts: WatchProviderConnectionConfig,
): RenderableConnectionSchema[] {
  return schemas.filter(
    (schema): schema is RenderableConnectionSchema =>
      schema.admin_form != null && (schema.required || hasEnteredValue(drafts[schema.key])),
  );
}

export function connectionSchemasAreValid(
  schemas: PluginConfigSchema[],
  drafts: WatchProviderConnectionConfig,
  validity: Record<string, boolean>,
): boolean {
  return activeConnectionSchemas(schemas, drafts).every((schema) => validity[schema.key] ?? false);
}

export function buildConnectionConfig(
  schemas: PluginConfigSchema[],
  drafts: WatchProviderConnectionConfig,
): WatchProviderConnectionConfig {
  return Object.fromEntries(
    activeConnectionSchemas(schemas, drafts).map((schema) => [
      schema.key,
      buildSchemaValues(
        schema.admin_form,
        drafts[schema.key] ?? {},
        parseFieldTypes(schema.json_schema),
      ),
    ]),
  );
}
