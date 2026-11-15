import { Switch } from "@/components/ui/switch";
import { useT, type MessageKey } from "@/i18n";
import { PERMISSION_MARKER_EDIT, PERMISSION_METADATA_CURATION } from "@/lib/permissions";

import { ToggleRow } from "./AccessGroupFields";

// The two assignable permissions (mirrors auth.assignablePermissions). A group
// mask of `null` means "all assignable"; a list narrows to those named.
const ASSIGNABLE_PERMISSIONS: Array<{
  value: string;
  label: MessageKey;
  description: MessageKey;
}> = [
  {
    value: PERMISSION_METADATA_CURATION,
    label: "web.admin.access.groups.permissions.metadataCuration",
    description: "web.admin.access.groups.permissions.metadataCurationDescription",
  },
  {
    value: PERMISSION_MARKER_EDIT,
    label: "web.admin.access.groups.permissions.markerEdit",
    description: "web.admin.access.groups.permissions.markerEditDescription",
  },
];

/** The group's permission mask: `null` allows every assignable permission. */
export function AccessGroupPermissionsSection({
  permissions,
  onChange,
}: {
  permissions: string[] | null;
  onChange: (next: string[] | null) => void;
}) {
  const t = useT();
  const allPermissions = permissions === null;

  function setPermissionAllowed(permission: string, allowed: boolean) {
    const current = permissions ?? ASSIGNABLE_PERMISSIONS.map((entry) => entry.value);
    const next = allowed
      ? Array.from(new Set([...current, permission]))
      : current.filter((value) => value !== permission);
    onChange(next);
  }

  return (
    <section className="surface-panel space-y-3 rounded-2xl border-0 p-5">
      <div className="flex items-center justify-between">
        <div>
          <h2 className="text-sm font-semibold">
            {t("web.admin.access.groups.permissions.heading")}
          </h2>
          <p className="text-muted-foreground mt-0.5 text-xs">
            {t("web.admin.access.groups.permissions.help")}
          </p>
        </div>
        <div className="flex items-center gap-2">
          <span className="text-muted-foreground text-xs">
            {t("web.admin.access.groups.permissions.allowAll")}
          </span>
          <Switch
            checked={allPermissions}
            onCheckedChange={(checked) =>
              onChange(checked ? null : ASSIGNABLE_PERMISSIONS.map((entry) => entry.value))
            }
            aria-label={t("web.admin.access.groups.permissions.allowAllLabel")}
          />
        </div>
      </div>
      {!allPermissions && (
        <div className="space-y-2">
          {ASSIGNABLE_PERMISSIONS.map((permission) => (
            <ToggleRow
              key={permission.value}
              label={t(permission.label)}
              description={t(permission.description)}
              checked={permissions?.includes(permission.value) ?? false}
              onCheckedChange={(checked) => setPermissionAllowed(permission.value, checked)}
            />
          ))}
        </div>
      )}
    </section>
  );
}
