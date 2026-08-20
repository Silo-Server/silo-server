import type { PluginAdminForm, PluginAdminFormField, PluginConfigSchema } from "@/api/types";

export function humanizeConfigKey(value: string) {
  return value
    .split("_")
    .filter(Boolean)
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(" ");
}

export function adminFormForConfigSchema(schema: PluginConfigSchema): PluginAdminForm | null {
  const explicitFields = schema.admin_form?.fields ?? [];
  try {
    const parsed = JSON.parse(schema.json_schema) as {
      type?: string;
      required?: string[];
      properties?: Record<
        string,
        {
          type?: string;
          title?: string;
          description?: string;
          writeOnly?: boolean;
          format?: string;
          default?: unknown;
        }
      >;
    };
    if (parsed.type !== "object" || !parsed.properties) {
      return explicitFields.length > 0 ? schema.admin_form! : null;
    }

    const inferredFields = Object.entries(parsed.properties).map(
      ([key, property]): PluginAdminFormField | null => {
        const propertyType = property.type;
        if (!propertyType || !["string", "number", "integer", "boolean"].includes(propertyType)) {
          return null;
        }
        const secret = property.writeOnly === true || property.format === "password";
        const control =
          propertyType === "boolean"
            ? "SWITCH"
            : propertyType === "number" || propertyType === "integer"
              ? "NUMBER"
              : secret
                ? "PASSWORD"
                : "TEXT";
        return {
          key,
          label: property.title || humanizeConfigKey(key),
          description: property.description,
          control,
          placeholder: "",
          required: parsed.required?.includes(key) ?? false,
          secret,
          multiline: false,
          default_value:
            typeof property.default === "string" ||
            typeof property.default === "number" ||
            typeof property.default === "boolean"
              ? property.default
              : undefined,
          options: [],
          rows: 0,
        };
      },
    );

    if (explicitFields.length > 0) {
      const explicitKeys = new Set(explicitFields.map((field) => field.key));
      const sensitiveKeys = new Set(
        Object.entries(parsed.properties)
          .filter(([, property]) => property.writeOnly === true || property.format === "password")
          .map(([key]) => key),
      );
      return {
        ...schema.admin_form,
        fields: [
          ...explicitFields.map((field) =>
            sensitiveKeys.has(field.key) ? { ...field, secret: true } : field,
          ),
          ...inferredFields.filter(
            (field): field is PluginAdminFormField => field != null && !explicitKeys.has(field.key),
          ),
        ],
      };
    }

    if (inferredFields.some((field) => field == null)) return null;

    return {
      ...schema.admin_form,
      fields: inferredFields.filter((field): field is PluginAdminFormField => field != null),
    };
  } catch {
    return explicitFields.length > 0 ? schema.admin_form! : null;
  }
}
