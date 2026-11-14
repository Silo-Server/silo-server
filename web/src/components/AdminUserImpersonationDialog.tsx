import { useRef, useState } from "react";
import { useNavigate } from "react-router";
import type { AdminUser } from "@/api/types";
import { captureAdminUserAuthority, requireAdminUserAuthority } from "@/api/v2/adminUsers";
import { useAdminUserCapabilities, useImpersonateUser } from "@/hooks/queries/admin/users";
import { useAuth } from "@/hooks/useAuth";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { useT } from "@/i18n";
import { TOUCH_TARGETS } from "@/lib/touchTargets";

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
  const t = useT();
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
      title={t("web.admin.users.impersonateDialog.title")}
      description={
        user.role === "admin"
          ? t("web.admin.users.impersonateDialog.descriptionAdmin", { username: user.username })
          : t("web.admin.users.impersonateDialog.descriptionUser", { username: user.username })
      }
      confirmLabel={t("web.admin.users.impersonateDialog.confirm")}
      className={TOUCH_TARGETS}
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
            onError(
              err instanceof Error ? err.message : t("web.admin.users.impersonateDialog.failed"),
            );
          })
          .finally(() => {
            busy.current = false;
          });
      }}
    />
  );
}
