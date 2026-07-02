import { RegoEditor } from "@/components/policy/RegoEditor";
import { usePolicyVendor } from "@/hooks/queries/admin/policy";

export function PolicyVendorViewer() {
  const { data: modules, isLoading, error } = usePolicyVendor();

  if (isLoading) {
    return <p className="text-muted-foreground text-sm">Loading vendor policy modules...</p>;
  }

  if (error) {
    return (
      <div className="border-destructive/40 bg-destructive/10 text-destructive rounded-lg border px-3 py-2 text-sm">
        Failed to load vendor policy modules.
      </div>
    );
  }

  if (!modules?.length) {
    return (
      <div className="surface-panel-subtle text-muted-foreground rounded-2xl p-6 text-sm">
        No vendor policy modules are available.
      </div>
    );
  }

  return (
    <div className="space-y-5">
      {modules.map((module) => (
        <section key={module.path} className="space-y-2">
          <div className="flex items-center justify-between gap-3">
            <h2 className="font-mono text-sm font-semibold">{module.path}</h2>
          </div>
          <RegoEditor value={module.source} readOnly height="320px" />
        </section>
      ))}
    </div>
  );
}
