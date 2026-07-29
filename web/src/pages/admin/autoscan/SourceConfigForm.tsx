import { useMemo } from "react";
import { Library as LibraryIcon } from "lucide-react";

import type { AutoscanScanSourceDescriptor } from "@/api/types";
import { Button } from "@/components/ui/button";
import { SchemaForm } from "@/components/admin/plugins/SchemaForm";
import { useAdminLibraries } from "@/hooks/queries/admin/libraries";

import { configFields, fillValueFromLibraries } from "./sourceDescriptor";

/**
 * Renders a scan source's per-source configuration from its descriptor, using
 * the same schema renderer as plugin config. Nothing here knows which plugin it
 * is drawing — that is the point: a new scan-source plugin ships its own fields
 * in its manifest and they appear here without a frontend change.
 *
 * `source_config` is a string map on the wire, so values are marshalled to and
 * from strings at this boundary.
 */
export function SourceConfigForm({
  descriptor,
  values,
  onChange,
  idPrefix = "source-config",
}: {
  descriptor: AutoscanScanSourceDescriptor;
  values: Record<string, string>;
  onChange: (next: Record<string, string>) => void;
  idPrefix?: string;
}) {
  const libraries = useAdminLibraries();
  const form = descriptor.config_form;
  const fields = configFields(descriptor);

  // Fields that can be populated from a host-known value, paired with what that
  // value currently is. Computed together so the button can be disabled when
  // there is genuinely nothing to fill in.
  const fillable = useMemo(() => {
    return fields
      .map((field) => ({
        field,
        value: fillValueFromLibraries(field.fill_from, libraries.data ?? []),
      }))
      .filter((entry): entry is { field: (typeof fields)[number]; value: string } =>
        Boolean(entry.value),
      );
  }, [fields, libraries.data]);

  if (!form || fields.length === 0) {
    return null;
  }

  function applyFill(key: string, value: string) {
    onChange({ ...values, [key]: value });
  }

  return (
    <div className="space-y-3">
      <SchemaForm
        descriptor={form}
        values={values}
        onChange={(next) => {
          const out: Record<string, string> = {};
          for (const [key, value] of Object.entries(next)) {
            if (value === undefined || value === null) continue;
            out[key] = String(value);
          }
          onChange(out);
        }}
        idPrefix={idPrefix}
      />

      {fillable.length > 0 && (
        <div className="flex flex-wrap gap-2">
          {fillable.map(({ field, value }) => (
            <Button
              key={field.key}
              type="button"
              variant="outline"
              size="sm"
              onClick={() => applyFill(field.key, value)}
              title={`Replace ${field.label} with the paths of your enabled libraries`}
            >
              <LibraryIcon className="size-3.5" />
              Use library paths for {field.label}
            </Button>
          ))}
        </div>
      )}
    </div>
  );
}

export default SourceConfigForm;
