import { useRef, useState } from "react";
import { useNavigate } from "react-router";
import type { AdminUser } from "@/api/types";
import { captureAdminUserAuthority, requireAdminUserAuthority } from "@/api/v2/adminUsers";
import { useAdminUserCapabilities, useImpersonateUser } from "@/hooks/queries/admin/users";
import { useAuth } from "@/hooks/useAuth";
import { ConfirmDialog } from "@/components/ConfirmDialog";

export function AdminUserImpersonationDialog({
  user,
  returnPath,
  onClose,
  onError,
}: {
  user: AdminUser;
  returnPath: string;
  onClose: () => void;
  onError: (message: string) => void;
}) {
  const navigate = useNavigate();
  const { beginImpersonation } = useAuth();
  const [authority] = useState(captureAdminUserAuthority);
  const busy = useRef(false);
  const capabilities = useAdminUserCapabilities();
  const impersonateMutation = useImpersonateUser();

  return (
    <ConfirmDialog
      open
      onOpenChange={(open) => {
        if (!open && !busy.current) onClose();
      }}
      title="View as user"
      description={
        user.role === "admin"
          ? `Continue as "${user.username}"? Actions you take will run as this admin, with their access, until you end this session.`
          : `Continue as "${user.username}"? Actions you take will run as this user. Admin access will be unavailable until you end this session.`
      }
      confirmLabel="View as user"
      isPending={impersonateMutation.isPending}
      onConfirm={() => {
        // Callers offer only accounts the viewer may view as
        // (canViewAsAccount); the server enforces the same rules.
        if (busy.current || capabilities.data?.available !== true || !user.enabled) return;
        busy.current = true;
        onError("");
        void impersonateMutation
          .mutateAsync({ id: user.id, profileContext: authority })
          .then((result) => {
            requireAdminUserAuthority(result.profileContext);
            beginImpersonation(result.session, returnPath);
            impersonateMutation.reset();
            onClose();
            navigate("/profiles");
          })
          .catch((err: unknown) => {
            onError(err instanceof Error ? err.message : "Could not start viewing as this user.");
          })
          .finally(() => {
            busy.current = false;
          });
      }}
    />
  );
}
