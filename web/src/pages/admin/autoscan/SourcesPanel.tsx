import { useMemo, useState } from "react";
import { Plus } from "lucide-react";
import { Link } from "react-router";

import { captureProfileRequestContext, isCapturedProfileAuthorityActive } from "@/api/client";
import type { AutoscanScanSourceDescriptor, AutoscanSource } from "@/api/types";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { useAdminLibraries } from "@/hooks/queries/admin/libraries";
import {
  captureSourceDeletion,
  type AutoscanSourceDeleteIntent,
  useAutoscanConnections,
  useAutoscanSettings,
  useAutoscanSources,
  useAvailableScanSources,
  useDeleteAutoscanSource,
} from "@/hooks/queries/useAutoscan";
import { buildPluginDisplayNames } from "@/lib/autoscanLabels";

import { SourceDialog } from "./SourceDialog";
import { DEFAULT_DESCRIPTOR, descriptorFor } from "./sourceDescriptor";
import { describeSource, type SourceDisplay } from "./sourceDisplay";
import { pluginKey } from "./sourceForm";
import { SourceList, SourceListRow } from "./SourceList";
import { describeTargets, sourceTargets } from "./sourceTargets";

export default function SourcesPanel() {
  const sources = useAutoscanSources();
  const connections = useAutoscanConnections();
  const settings = useAutoscanSettings();
  const available = useAvailableScanSources();
  const libraries = useAdminLibraries();
  const deleteSource = useDeleteAutoscanSource();

  const pluginDisplayNames = useMemo(
    () => buildPluginDisplayNames(available.data ?? []),
    [available.data],
  );

  const connectionOptions = (connections.data ?? []).map((c) => ({
    id: c.id,
    name: c.name,
    kind: c.kind,
    requestIntegrationId: c.request_integration_id ?? null,
  }));
  const connectionNames = new Map(connectionOptions.map((c) => [c.id, c.name]));
  const globalPollInterval = settings.data?.default_poll_interval_seconds ?? null;

  // Descriptor per installed capability. A source whose capability is no
  // longer installed falls back to the defaults rather than disappearing.
  const descriptorsByKey = useMemo(() => {
    const map = new Map<string, AutoscanScanSourceDescriptor>();
    for (const plugin of available.data ?? []) {
      map.set(pluginKey(plugin.plugin_id, plugin.capability_id), descriptorFor(plugin));
    }
    return map;
  }, [available.data]);

  function descriptorForSource(source: AutoscanSource): AutoscanScanSourceDescriptor {
    return (
      descriptorsByKey.get(pluginKey(source.plugin_id, source.capability_id)) ?? DEFAULT_DESCRIPTOR
    );
  }

  function displayFor(source: AutoscanSource): SourceDisplay {
    return describeSource({
      source,
      pluginDisplayNames,
      connectionNames,
      defaultPollSeconds: globalPollInterval,
    });
  }

  const [addOpen, setAddOpen] = useState(false);
  // `session` remounts the edit dialog on every open so its draft starts from
  // the source as it is now; `open` is separate so closing still animates.
  const [editTarget, setEditTarget] = useState<{
    id: string;
    session: number;
    open: boolean;
  } | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<{
    source: AutoscanSource;
    intent: AutoscanSourceDeleteIntent;
  } | null>(null);

  const renderedAuthority = captureProfileRequestContext();
  const requestDelete = (source: AutoscanSource) => {
    if (!renderedAuthority || !isCapturedProfileAuthorityActive(renderedAuthority)) return;
    setDeleteTarget({ source, intent: captureSourceDeletion(source.id, renderedAuthority) });
  };
  const openEdit = (source: AutoscanSource) =>
    setEditTarget((current) => ({
      id: source.id,
      session: (current?.session ?? 0) + 1,
      open: true,
    }));

  if (sources.isLoading) {
    return <p className="text-muted-foreground py-4 text-sm">Loading sources…</p>;
  }

  // A failed refetch keeps the last list on screen. Replacing the panel with
  // the error would unmount the add dialog and lose its "Connect it" step.
  if (sources.isError && !sources.data) {
    return (
      <p className="text-destructive py-4 text-sm">
        Failed to load scan sources. Please reload the page.
      </p>
    );
  }

  const list = sources.data ?? [];
  // Another admin can delete the source while its confirmation is open.
  const deleteOpen =
    deleteTarget !== null && list.some((source) => source.id === deleteTarget.source.id);
  const editSource = editTarget ? list.find((s) => s.id === editTarget.id) : undefined;
  const editDisplay = editSource ? displayFor(editSource) : null;
  const editFeeds =
    editSource && !libraries.isLoading
      ? describeTargets(
          sourceTargets(editSource, descriptorForSource(editSource), libraries.data ?? []),
        )
      : null;

  // The add dialog stays at one position in the tree whether or not sources
  // exist. Creating the first source switches to the list; if the dialog moved
  // with it, React would remount it and drop the "Connect it" step.
  return (
    <div className="space-y-4">
      <div className="flex flex-col items-start gap-3 sm:flex-row sm:items-center sm:justify-between">
        <p className="text-muted-foreground text-xs">
          Scan-source plugins are installed from the{" "}
          <Link to="/admin/plugins" className="text-primary underline-offset-4 hover:underline">
            Plugins page
          </Link>
          . Add a source for each thing you want to watch.
        </p>
        <Button
          variant="outline"
          size="sm"
          className="w-full justify-center sm:w-auto sm:shrink-0"
          onClick={() => setAddOpen(true)}
        >
          <Plus />
          Add source
        </Button>
      </div>

      {sources.isError && (
        <p className="text-destructive text-sm">
          Could not refresh scan sources. Showing the last loaded list.
        </p>
      )}

      {list.length === 0 ? (
        <div className="rounded-lg border border-dashed p-8 text-center">
          <p className="text-muted-foreground text-sm">
            No scan sources yet. Click <span className="font-medium">Add source</span> to create one
            from an installed scan-source plugin.
          </p>
        </div>
      ) : (
        <SourceList>
          {list.map((source) => (
            <SourceListRow
              key={source.id}
              source={source}
              display={displayFor(source)}
              descriptor={descriptorForSource(source)}
              libraries={libraries.data ?? []}
              librariesLoading={libraries.isLoading}
              onEdit={openEdit}
              onRequestDelete={requestDelete}
            />
          ))}
        </SourceList>
      )}

      <SourceDialog
        mode="add"
        open={addOpen}
        onOpenChange={setAddOpen}
        connectionOptions={connectionOptions}
        globalPollInterval={globalPollInterval}
      />

      {editTarget && editSource && editDisplay && (
        <SourceDialog
          key={`${editTarget.id}:${editTarget.session}`}
          mode="edit"
          open={editTarget.open}
          onOpenChange={(open) => setEditTarget((t) => (t ? { ...t, open } : t))}
          source={editSource}
          title={editDisplay.title}
          description={[editDisplay.subtitle, editFeeds && `Feeds: ${editFeeds}`]
            .filter(Boolean)
            .join(" · ")}
          connectionOptions={connectionOptions}
          globalPollInterval={globalPollInterval}
          onRequestDelete={requestDelete}
        />
      )}

      <AlertDialog open={deleteOpen} onOpenChange={(open) => !open && setDeleteTarget(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete source?</AlertDialogTitle>
            <AlertDialogDescription>
              &ldquo;{deleteTarget ? displayFor(deleteTarget.source).title : ""}&rdquo; will be
              permanently removed. This cannot be undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={() => {
                if (deleteTarget) {
                  deleteSource.mutateCaptured(deleteTarget.intent);
                  // The edit dialog for a deleted source has nothing left to save.
                  setEditTarget((t) =>
                    t && t.id === deleteTarget.source.id ? { ...t, open: false } : t,
                  );
                  setDeleteTarget(null);
                }
              }}
            >
              Delete
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
