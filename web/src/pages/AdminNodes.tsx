import { useState } from "react";
import type { FormEvent, ReactNode } from "react";
import type { StreamNode, CreateNodeRequest } from "@/api/types";
import {
  useAdminNodes,
  useCreateNode,
  useUpdateNode,
  useDeleteNode,
  useCheckNodeHealth,
  useToggleNode,
} from "@/hooks/queries/admin/nodes";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Badge } from "@/components/ui/badge";
import { Switch } from "@/components/ui/switch";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Plus, Pencil, Trash2, RefreshCw, Info, AlertTriangle } from "lucide-react";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { formatDateTime } from "@/lib/datetime";

type NodeType = "proxy" | "transcode";

function formatMbps(kbps: number): string {
  return (Math.round(kbps / 100) / 10).toString();
}

interface NodeSectionProps {
  type: NodeType;
  nodes: StreamNode[];
  infoBanner: ReactNode;
  showJobs: boolean;
  onAdd: () => void;
  onEdit: (node: StreamNode) => void;
  onDelete: (node: StreamNode) => void;
  onToggle: (node: StreamNode) => void;
  onCheckHealth: (node: StreamNode) => void;
  checkingHealthId: number | null;
}

function NodeSection({
  type,
  nodes,
  infoBanner,
  showJobs,
  onAdd,
  onEdit,
  onDelete,
  onToggle,
  onCheckHealth,
  checkingHealthId,
}: NodeSectionProps) {
  const label = type === "proxy" ? "Proxy" : "Transcode";
  const colCount = (showJobs ? 8 : 7) + (type === "proxy" ? 1 : 0);

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex items-center gap-2">
          <h2 className="text-lg font-semibold">{label} Nodes</h2>
          <Badge variant="secondary">{nodes.length}</Badge>
        </div>
        <Button size="sm" onClick={onAdd}>
          <Plus className="mr-1 h-4 w-4" /> Add {label}
        </Button>
      </div>

      {infoBanner}

      <div className="surface-panel overflow-x-auto rounded-xl border-0">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead>URL</TableHead>
              <TableHead>Group</TableHead>
              <TableHead>Status</TableHead>
              <TableHead>Health</TableHead>
              {showJobs && <TableHead>{type === "proxy" ? "Streams" : "Jobs"}</TableHead>}
              {type === "proxy" && <TableHead>Egress</TableHead>}
              <TableHead>Last Check</TableHead>
              <TableHead className="w-32">Actions</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {nodes.length === 0 ? (
              <TableRow>
                <TableCell colSpan={colCount} className="text-muted-foreground py-8 text-center">
                  <div className="space-y-2">
                    <p>
                      {type === "proxy"
                        ? "No proxy nodes configured. Add a proxy node to enable distributed stream delivery."
                        : "No transcode nodes configured. Add a transcode node to offload video transcoding from the main server."}
                    </p>
                    <Button variant="outline" size="sm" onClick={onAdd}>
                      <Plus className="mr-1 h-4 w-4" /> Add {label}
                    </Button>
                  </div>
                </TableCell>
              </TableRow>
            ) : (
              nodes.map((node) => {
                const isChecking = checkingHealthId === node.id;
                return (
                  <TableRow key={node.id}>
                    <TableCell className="font-medium">{node.name}</TableCell>
                    <TableCell className="font-mono text-sm">{node.url}</TableCell>
                    <TableCell>
                      {node.group ? (
                        <Badge variant="outline">{node.group}</Badge>
                      ) : (
                        <span className="text-muted-foreground">—</span>
                      )}
                    </TableCell>
                    <TableCell>
                      <Switch checked={node.enabled} onCheckedChange={() => onToggle(node)} />
                    </TableCell>
                    <TableCell>
                      <span className="flex items-center gap-1.5">
                        <span
                          className={`h-2.5 w-2.5 rounded-full ${
                            !node.enabled
                              ? "bg-muted-foreground"
                              : node.healthy
                                ? "bg-success"
                                : "bg-destructive"
                          }`}
                        />
                        <span className="text-muted-foreground text-sm">
                          {!node.enabled ? "Disabled" : node.healthy ? "Healthy" : "Unhealthy"}
                        </span>
                      </span>
                    </TableCell>
                    {showJobs && (
                      <TableCell>
                        {node.active_jobs}
                        {node.max_jobs != null && (
                          <span className="text-muted-foreground"> / {node.max_jobs}</span>
                        )}
                      </TableCell>
                    )}
                    {type === "proxy" && (
                      <TableCell className="text-sm whitespace-nowrap">
                        {formatMbps(node.egress_kbps)}
                        {node.max_bandwidth_kbps != null && (
                          <span className="text-muted-foreground">
                            {" "}
                            / {formatMbps(node.max_bandwidth_kbps)}
                          </span>
                        )}{" "}
                        Mbps
                      </TableCell>
                    )}
                    <TableCell className="text-muted-foreground text-xs">
                      {node.last_health_check ? formatDateTime(node.last_health_check) : "Never"}
                    </TableCell>
                    <TableCell>
                      <div className="flex gap-1">
                        <Button
                          variant="ghost"
                          size="icon"
                          className="h-7 w-7"
                          disabled={isChecking}
                          aria-label={`Check health of ${node.name}`}
                          onClick={() => onCheckHealth(node)}
                        >
                          <RefreshCw
                            className={`h-3 w-3 ${isChecking ? "animate-spin" : ""}`}
                            aria-hidden="true"
                          />
                        </Button>
                        <Button
                          variant="ghost"
                          size="icon"
                          className="h-7 w-7"
                          aria-label={`Edit ${node.name}`}
                          onClick={() => onEdit(node)}
                        >
                          <Pencil className="h-3 w-3" aria-hidden="true" />
                        </Button>
                        <Button
                          variant="ghost"
                          size="icon"
                          className="h-7 w-7"
                          aria-label={`Delete ${node.name}`}
                          onClick={() => onDelete(node)}
                        >
                          <Trash2 className="h-3 w-3" aria-hidden="true" />
                        </Button>
                      </div>
                    </TableCell>
                  </TableRow>
                );
              })
            )}
          </TableBody>
        </Table>
      </div>
    </div>
  );
}

function NodeForm({
  node,
  nodeType,
  onClose,
}: {
  node: StreamNode | null;
  nodeType: NodeType;
  onClose: () => void;
}) {
  const [name, setName] = useState(node?.name ?? "");
  const [url, setUrl] = useState(node?.url ?? "");
  const [group, setGroup] = useState(node?.group ?? "");
  const [maxJobs, setMaxJobs] = useState(node?.max_jobs?.toString() ?? "");
  const [maxBandwidthMbps, setMaxBandwidthMbps] = useState(
    node?.max_bandwidth_kbps ? (node.max_bandwidth_kbps / 1000).toString() : "",
  );
  const createMutation = useCreateNode();
  const updateMutation = useUpdateNode();
  const isPending = createMutation.isPending || updateMutation.isPending;

  const urlPlaceholder =
    nodeType === "proxy" ? "https://proxy1.example.com" : "http://10.0.0.5:8082";

  async function handleSubmit(e: FormEvent) {
    e.preventDefault();
    // The backend treats an empty group as "ungrouped" and caps <= 0 as
    // "unlimited", so cleared inputs reset those fields.
    const parsedMaxJobs = parseInt(maxJobs, 10);
    const parsedMaxBandwidthMbps = parseFloat(maxBandwidthMbps);
    const fields = {
      name,
      url,
      group: group.trim(),
      max_jobs: Number.isNaN(parsedMaxJobs) ? 0 : parsedMaxJobs,
      max_bandwidth_kbps: Number.isNaN(parsedMaxBandwidthMbps)
        ? 0
        : Math.round(parsedMaxBandwidthMbps * 1000),
    };
    if (node) {
      updateMutation.mutate({ id: node.id, body: fields }, { onSuccess: onClose });
    } else {
      const body: CreateNodeRequest = { type: nodeType, ...fields };
      createMutation.mutate(body, { onSuccess: onClose });
    }
  }

  return (
    <form onSubmit={handleSubmit} className="space-y-4">
      <div className="space-y-2">
        <Label>Name</Label>
        <Input
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder={nodeType === "proxy" ? "Proxy Node 1" : "Transcode Node 1"}
          required
        />
      </div>

      <div className="space-y-2">
        <Label>Type</Label>
        <Badge variant="secondary" className="text-sm">
          {nodeType === "proxy" ? "Proxy" : "Transcode"}
        </Badge>
      </div>

      <div className="space-y-2">
        <Label>URL</Label>
        <Input
          value={url}
          onChange={(e) => setUrl(e.target.value)}
          placeholder={urlPlaceholder}
          required
        />
        {nodeType === "transcode" ? (
          <p className="text-muted-foreground text-sm">
            Must be reachable from proxy nodes and the backend server. A private/internal IP or
            localhost is fine — no public URL needed.
          </p>
        ) : (
          <p className="text-muted-foreground text-sm">
            Must be publicly accessible by streaming clients.
          </p>
        )}
      </div>

      <div className="space-y-2">
        <Label>Group</Label>
        <Input value={group} onChange={(e) => setGroup(e.target.value)} placeholder="e.g. rack-1" />
        <p className="text-muted-foreground text-sm">
          Optional. Nodes in the same group are treated as co-located: transcoded streams are served
          by a proxy from the transcode node's group, keeping traffic on the same LAN. A group is
          only used while all of its nodes are healthy.
        </p>
      </div>

      <div className="space-y-2">
        <Label>{nodeType === "proxy" ? "Max Streams" : "Max Transcodes"}</Label>
        <Input
          type="number"
          min={0}
          value={maxJobs}
          onChange={(e) => setMaxJobs(e.target.value)}
          placeholder="Unlimited"
        />
        <p className="text-muted-foreground text-sm">
          Optional concurrency cap for this node. Leave empty (or 0) for unlimited.
        </p>
      </div>

      {nodeType === "proxy" && (
        <div className="space-y-2">
          <Label>Max Egress Bandwidth (Mbps)</Label>
          <Input
            type="number"
            min={0}
            step="any"
            value={maxBandwidthMbps}
            onChange={(e) => setMaxBandwidthMbps(e.target.value)}
            placeholder="Unlimited"
          />
          <p className="text-muted-foreground text-sm">
            Optional. New streams are routed elsewhere once this node's measured egress (plus the
            expected bitrate of the new stream) would exceed the cap. Active streams are never
            interrupted. Leave empty (or 0) for unlimited.
          </p>
        </div>
      )}

      <Button type="submit" className="w-full" disabled={isPending}>
        {isPending ? "Saving..." : "Save"}
      </Button>
    </form>
  );
}

export default function AdminNodes() {
  const { data: nodes = [], isLoading } = useAdminNodes();
  const [dialogOpen, setDialogOpen] = useState(false);
  const [editingNode, setEditingNode] = useState<StreamNode | null>(null);
  const [addingNodeType, setAddingNodeType] = useState<NodeType | null>(null);
  const [confirmDeleteNode, setConfirmDeleteNode] = useState<StreamNode | null>(null);
  const deleteMutation = useDeleteNode();
  const checkHealthMutation = useCheckNodeHealth();
  const toggleMutation = useToggleNode();

  const proxyNodes = nodes.filter((n) => n.type === "proxy");
  const transcodeNodes = nodes.filter((n) => n.type === "transcode");

  const checkingHealthId =
    checkHealthMutation.isPending && checkHealthMutation.variables
      ? checkHealthMutation.variables.id
      : null;

  const resolvedNodeType: NodeType = editingNode
    ? (editingNode.type as NodeType)
    : (addingNodeType ?? "proxy");

  function handleAdd(type: NodeType) {
    setAddingNodeType(type);
    setEditingNode(null);
    setDialogOpen(true);
  }

  function handleEdit(node: StreamNode) {
    setEditingNode(node);
    setAddingNodeType(null);
    setDialogOpen(true);
  }

  function handleDelete(node: StreamNode) {
    setConfirmDeleteNode(node);
  }

  function handleDialogChange(open: boolean) {
    setDialogOpen(open);
    if (!open) {
      setEditingNode(null);
      setAddingNodeType(null);
    }
  }

  if (isLoading) return <div className="page-shell py-8">Loading nodes...</div>;

  return (
    <div className="page-shell space-y-8 py-4 sm:py-6">
      <div className="page-header gap-5">
        <div className="space-y-3">
          <h1 className="page-title text-[clamp(2rem,4vw,3rem)]">Stream Nodes</h1>
          <p className="page-subtitle text-sm sm:text-base">
            Manage proxy and transcode workers that distribute playback load across your
            infrastructure.
          </p>
        </div>
      </div>

      <ConfirmDialog
        open={confirmDeleteNode !== null}
        onOpenChange={(open) => {
          if (!open) setConfirmDeleteNode(null);
        }}
        title="Delete node"
        description={`Delete stream node "${confirmDeleteNode?.name}"? This action cannot be undone.`}
        confirmLabel="Delete"
        variant="destructive"
        onConfirm={() => {
          if (confirmDeleteNode) deleteMutation.mutate(confirmDeleteNode.id);
          setConfirmDeleteNode(null);
        }}
      />

      <NodeSection
        type="proxy"
        nodes={proxyNodes}
        showJobs={true}
        onAdd={() => handleAdd("proxy")}
        onEdit={handleEdit}
        onDelete={handleDelete}
        onToggle={(node) => toggleMutation.mutate(node)}
        onCheckHealth={(node) => checkHealthMutation.mutate(node)}
        checkingHealthId={checkingHealthId}
        infoBanner={
          <div className="surface-panel-subtle text-info flex items-start gap-2 rounded-xl p-3 text-sm">
            <Info className="mt-0.5 h-4 w-4 shrink-0" />
            <p>Proxy nodes relay streams to end users. The URL must be publicly accessible.</p>
          </div>
        }
      />

      <NodeSection
        type="transcode"
        nodes={transcodeNodes}
        showJobs={true}
        onAdd={() => handleAdd("transcode")}
        onEdit={handleEdit}
        onDelete={handleDelete}
        onToggle={(node) => toggleMutation.mutate(node)}
        onCheckHealth={(node) => checkHealthMutation.mutate(node)}
        checkingHealthId={checkingHealthId}
        infoBanner={
          <div className="surface-panel-subtle text-warning flex items-start gap-2 rounded-xl p-3 text-sm">
            <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0" />
            <p>
              Transcode nodes handle video transcoding internally.{" "}
              <strong>Must be on the same network as proxy nodes and the backend.</strong> Does not
              need a public URL.
            </p>
          </div>
        }
      />

      <Dialog open={dialogOpen} onOpenChange={handleDialogChange}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>
              {editingNode
                ? "Edit Node"
                : `Add ${resolvedNodeType === "proxy" ? "Proxy" : "Transcode"} Node`}
            </DialogTitle>
          </DialogHeader>
          <NodeForm
            node={editingNode}
            nodeType={resolvedNodeType}
            onClose={() => handleDialogChange(false)}
          />
        </DialogContent>
      </Dialog>
    </div>
  );
}
