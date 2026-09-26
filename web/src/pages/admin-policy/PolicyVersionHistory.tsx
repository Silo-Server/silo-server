import { policyApplyMessage, policyMutationMessage, type PolicySnapshot } from "@/api/adminPolicy";
import { PolicyRevisionReview } from "./PolicyRevisionReview";
import { RotateCcw } from "lucide-react";
import { useMemo, useState } from "react";

import { RegoEditor } from "@/components/policy/RegoEditor";
import {
  AlertDialog,
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
  usePolicyDocument,
} from "@/hooks/queries/admin/policy";

import { formatPolicyDate, messageFromError } from "./policyPageUtils";

interface PolicyVersionHistoryProps {
  documentId: string;
  activeVersionId?: string;
}

export function PolicyVersionHistory({ documentId, activeVersionId }: PolicyVersionHistoryProps) {
  const history = usePolicyVersions(documentId);
  const { data: versions, isLoading } = history;
  const canonical = usePolicyDocument(documentId);
  const [captured, setCaptured] = useState<PolicySnapshot>();
  const [message, setMessage] = useState("");
  const [needsReview, setNeedsReview] = useState(false);
  const [selectedVersionId, setSelectedVersionId] = useState<string | undefined>(undefined);
  const [rollbackVersionId, setRollbackVersionId] = useState<string | undefined>(undefined);
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
    if (!rollbackVersionId || !captured || needsReview) return;
    try {
      const result = await activate.mutateAsync({
        documentId,
        version: rollbackVersionId,
        etag: captured.etag,
      });
      setMessage(policyApplyMessage(result));
      setRollbackVersionId(undefined);
    } catch (error) {
      setNeedsReview(true);
      setMessage(policyMutationMessage(error, "Unable to activate policy version."));
    }
  }

  return (
    <div className="min-w-0 space-y-4">
      {message && <p role="status">{message}</p>}
      {history.error && (
        <div role="alert">
          <p>{messageFromError(history.error, "Unable to load version history.")}</p>
          <Button onClick={() => void history.restart()}>Restart version history</Button>
        </div>
      )}
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
                        Active
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
                      disabled={
                        isActive || !version.compiled_ok || !canonical.data || activate.isPending
                      }
                      onClick={(event) => {
                        event.stopPropagation();
                        setRollbackVersionId(version.id);
                        setCaptured(canonical.data);
                        setNeedsReview(false);
                        setMessage("");
                      }}
                    >
                      <RotateCcw className="size-4" />
                      Activate version
                    </Button>
                  </TableCell>
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
      </div>

      {history.hasNextPage && (
        <Button onClick={() => void history.fetchNextPage()} disabled={history.isFetchingNextPage}>
          Load older versions
        </Button>
      )}
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
            <AlertDialogTitle>Activate saved v{rollbackVersion?.version_number}?</AlertDialogTitle>
            <AlertDialogDescription>
              This saves the selected active version for {captured?.name}. Servers reload
              independently. The previous version remains in history.
            </AlertDialogDescription>
          </AlertDialogHeader>
          {needsReview && <p role="alert">{message}</p>}
          {needsReview && (
            <PolicyRevisionReview
              documentId={documentId}
              disabled={activate.isPending}
              onAdopt={(snapshot) => {
                setCaptured(snapshot);
                setNeedsReview(false);
              }}
            />
          )}
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <Button
              onClick={() => void confirmRollback()}
              disabled={activate.isPending || needsReview}
            >
              Activate
            </Button>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
