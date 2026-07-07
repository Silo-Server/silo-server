import { RotateCcw } from "lucide-react";
import { useMemo, useState } from "react";
import { toast } from "sonner";

import { RegoEditor } from "@/components/policy/RegoEditor";
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
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  useActivatePolicyVersion,
  usePolicyVersion,
  usePolicyVersions,
} from "@/hooks/queries/admin/policy";

import { formatPolicyDate, messageFromError } from "./policyPageUtils";

interface PolicyVersionHistoryProps {
  documentId: number;
  activeVersionId?: number;
}

export function PolicyVersionHistory({ documentId, activeVersionId }: PolicyVersionHistoryProps) {
  const { data: versions, isLoading } = usePolicyVersions(documentId);
  const [selectedVersionId, setSelectedVersionId] = useState<number | undefined>(undefined);
  const [rollbackVersionId, setRollbackVersionId] = useState<number | undefined>(undefined);
  const selectedVersion = usePolicyVersion(documentId, selectedVersionId);
  const activate = useActivatePolicyVersion();

  const effectiveSelectedId = selectedVersionId ?? versions?.[0]?.id;
  const effectiveVersion = usePolicyVersion(
    documentId,
    selectedVersionId === undefined ? versions?.[0]?.id : undefined,
  );
  const sourceVersion =
    selectedVersionId === undefined ? effectiveVersion.data : selectedVersion.data;

  const rollbackVersion = useMemo(
    () => versions?.find((version) => version.id === rollbackVersionId),
    [rollbackVersionId, versions],
  );

  async function confirmRollback() {
    if (!rollbackVersionId) return;
    try {
      await activate.mutateAsync({ documentId, version: rollbackVersionId });
      toast.success("Policy version activated");
      setRollbackVersionId(undefined);
    } catch (error) {
      toast.error(messageFromError(error, "Failed to activate policy version"));
    }
  }

  return (
    <div className="space-y-4">
      <div className="surface-panel-subtle overflow-hidden rounded-2xl">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Version</TableHead>
              <TableHead>Author</TableHead>
              <TableHead>Created</TableHead>
              <TableHead>Comment</TableHead>
              <TableHead>Compile</TableHead>
              <TableHead className="text-right">Actions</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {isLoading && (
              <TableRow>
                <TableCell colSpan={6} className="text-muted-foreground py-6 text-center">
                  Loading versions...
                </TableCell>
              </TableRow>
            )}
            {!isLoading && versions?.length === 0 && (
              <TableRow>
                <TableCell colSpan={6} className="text-muted-foreground py-6 text-center">
                  No versions have been saved for this document.
                </TableCell>
              </TableRow>
            )}
            {versions?.map((version) => {
              const isActive = version.id === activeVersionId;
              const isSelected = version.id === effectiveSelectedId;
              return (
                <TableRow
                  key={version.id}
                  data-state={isSelected ? "selected" : undefined}
                  className="cursor-pointer"
                  role="button"
                  tabIndex={0}
                  onClick={() => setSelectedVersionId(version.id)}
                  onKeyDown={(event) => {
                    if (event.key === "Enter" || event.key === " ") {
                      event.preventDefault();
                      setSelectedVersionId(version.id);
                    }
                  }}
                >
                  <TableCell className="font-medium">
                    v{version.version_number}
                    {isActive && (
                      <Badge variant="secondary" className="ml-2">
                        Live
                      </Badge>
                    )}
                  </TableCell>
                  <TableCell>
                    {version.created_by_user_id ? `User ${version.created_by_user_id}` : "—"}
                  </TableCell>
                  <TableCell>{formatPolicyDate(version.created_at)}</TableCell>
                  <TableCell className="max-w-[260px] truncate">
                    {version.comment?.trim() || "—"}
                  </TableCell>
                  <TableCell>
                    <Badge variant={version.compiled_ok ? "secondary" : "destructive"}>
                      {version.compiled_ok ? "Compiled" : "Failed"}
                    </Badge>
                  </TableCell>
                  <TableCell className="text-right">
                    <Button
                      type="button"
                      size="sm"
                      variant="outline"
                      disabled={isActive || !version.compiled_ok}
                      onClick={(event) => {
                        event.stopPropagation();
                        setRollbackVersionId(version.id);
                      }}
                    >
                      <RotateCcw className="size-4" />
                      Make live
                    </Button>
                  </TableCell>
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
      </div>

      {sourceVersion?.source !== undefined && (
        <div className="space-y-2">
          <h3 className="text-sm font-semibold">Selected Source</h3>
          <RegoEditor value={sourceVersion.source} readOnly height="260px" />
        </div>
      )}

      <AlertDialog
        open={rollbackVersionId !== undefined}
        onOpenChange={(open) => !open && setRollbackVersionId(undefined)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              Make v{rollbackVersion?.version_number} the live policy?
            </AlertDialogTitle>
            <AlertDialogDescription>
              New requests start using it immediately, on every server node. The version it replaces
              stays in this history.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction onClick={confirmRollback} disabled={activate.isPending}>
              Activate
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
