import { useId } from "react";

import type { PluginProvisionStatus } from "@/hooks/queries/admin/plugins";
import { useT, type MessageKey } from "@/i18n";

const stateLabels: ReadonlyMap<string, MessageKey> = new Map([
  ["pending", "web.admin.plugins.provisioning.pending"],
  ["refused", "web.admin.plugins.provisioning.refused"],
  ["held", "web.admin.plugins.provisioning.held"],
  ["applied", "web.admin.plugins.provisioning.applied"],
]);

export function PluginProvisioningStatus({ items }: { items?: PluginProvisionStatus[] }) {
  const t = useT();
  const titleId = useId();
  if (!items?.length) return null;

  return (
    <section aria-labelledby={titleId} className="bg-card space-y-2 rounded-xl border p-4">
      <h2 id={titleId} className="text-sm font-semibold">
        {t("web.admin.plugins.provisioning.title")}
      </h2>
      <ul className="divide-y">
        {items.map((record) => {
          const label = stateLabels.get(record.state);
          return (
            <li key={record.plugin_id} className="space-y-0.5 py-2 text-sm">
              <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
                <span className="font-medium">{record.plugin_id}</span>
                {record.version ? (
                  <span className="text-muted-foreground">
                    {t("web.admin.plugins.detail.version", { version: record.version })}
                  </span>
                ) : null}
                <span>{label ? t(label) : record.state}</span>
              </div>
              {record.detail ? (
                <p className="text-muted-foreground text-xs">{record.detail}</p>
              ) : null}
            </li>
          );
        })}
      </ul>
    </section>
  );
}
