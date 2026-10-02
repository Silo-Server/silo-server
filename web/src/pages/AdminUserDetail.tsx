import { useRef, useState } from "react";
import { useLocation, useNavigate, useParams, useSearchParams } from "react-router";
import { toast } from "sonner";

import {
  adminUserScope,
  captureAdminUserAuthority,
  getAdminUser,
  type AdminUserEditor,
} from "@/api/v2/adminUsers";
import { isNotFoundProblem, V2ProblemError } from "@/api/v2/request";
import { AdminUserDeleteDialog } from "@/components/AdminUserDeleteDialog";
import { AdminUserImpersonationDialog } from "@/components/AdminUserImpersonationDialog";
import { AdminUserPasswordResetDialog } from "@/components/AdminUserPasswordResetDialog";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import PageUnavailable from "@/components/PageUnavailable";
import ViewTransitionLink from "@/components/ViewTransitionLink";
import { Button } from "@/components/ui/button";
import { Tabs, TabsContent } from "@/components/ui/tabs";
import { useAccessGroups } from "@/hooks/queries/admin/accessGroups";
import { useAdminUserProfiles } from "@/hooks/queries/admin/history";
import {
  useAdminUser,
  useAdminUserCapabilities,
  useTransferOwnership,
  useUpdateUser,
  useViewerIsOwner,
} from "@/hooks/queries/admin/users";
import { useAuth } from "@/hooks/useAuth";
import {
  canChangeAccessPolicy,
  canManageAccount,
  canTransferOwnership,
  canViewAsAccount,
} from "@/lib/accountOwner";
import { guardRedirectTarget } from "@/lib/authRedirect";

import { AccessTab } from "./admin-users/detail/access/AccessTab";
import { ActivityTab } from "./admin-users/detail/activity/ActivityTab";
import { CardEditingProvider } from "./admin-users/detail/cardEditing";
import { DownloadsTab } from "./admin-users/detail/downloads/DownloadsTab";
import { OverviewTab } from "./admin-users/detail/overview/OverviewTab";
import { PreferencesTab } from "./admin-users/detail/preferences/PreferencesTab";
import { UnsavedCardGuard } from "./admin-users/detail/UnsavedCardGuard";
import { UserDetailHeader } from "./admin-users/detail/UserDetailHeader";
import { UserDetailTabs } from "./admin-users/detail/UserDetailTabs";
import { parseUserDetailTab, userDetailTabSearch } from "./admin-users/detail/userDetailTabs";

export default function AdminUserDetail() {
  useAuth();
  const { id } = useParams<{ id: string }>();
  // A new account, or the same one under another profile's authority, starts clean.
  return <AdminUserDetailPage key={`${adminUserScope()}:${id}`} />;
}

function AdminUserDetailPage() {
  const { id } = useParams<{ id: string }>();
  const userId = Number(id);
  const navigate = useNavigate();
  const location = useLocation();
  const [searchParams, setSearchParams] = useSearchParams();
  const { data: cachedUser, editor, isLoading, isFetching, error, refetch } = useAdminUser(userId);
  const viewerId = useAuth().user?.id;
  const viewerIsOwner = useViewerIsOwner(viewerId);
  // A background read that fails leaves the loaded account up, but a 404 means
  // it is gone (another admin deleted it) and outranks the cached copy.
  const user = isNotFoundProblem(error) ? undefined : cachedUser;
  const [authority] = useState(captureAdminUserAuthority);
  const busy = useRef(false);
  const [actionError, setActionError] = useState("");
  const capabilities = useAdminUserCapabilities();
  const available = capabilities.data?.available === true;
  const groups = useAccessGroups().data ?? [];
  const profiles = useAdminUserProfiles(userId);
  const updateUser = useUpdateUser();
  const transferOwnership = useTransferOwnership();
  const [confirmImpersonateOpen, setConfirmImpersonateOpen] = useState(false);
  const [resetOpen, setResetOpen] = useState(false);
  const [transferOpen, setTransferOpen] = useState(false);
  const [disableOpen, setDisableOpen] = useState(false);
  const [deleteEditor, setDeleteEditor] = useState<AdminUserEditor | null>(null);

  if (isLoading) return <div className="page-shell py-8">Loading user...</div>;
  if (!user) {
    if (error && !isNotFoundProblem(error)) {
      return (
        <PageUnavailable
          title="Couldn't load this user"
          description="Something went wrong while loading the account. Try again in a moment."
          onRetry={() => void refetch()}
          retrying={isFetching}
        />
      );
    }
    // With a valid id and no error, the read never ran: account reads act as a
    // profile, and none is selected. Nothing says the account is gone.
    if (!error && Number.isSafeInteger(userId) && userId > 0) {
      return (
        <PageUnavailable
          title="Choose a profile first"
          description="Managing accounts acts as one of your profiles. Choose a profile, then open this account again."
        >
          <Button asChild variant="outline">
            <ViewTransitionLink to={guardRedirectTarget("/profiles", location)}>
              Choose profile
            </ViewTransitionLink>
          </Button>
        </PageUnavailable>
      );
    }
    return (
      <PageUnavailable
        title="User not found"
        description="The account may have been deleted, or the link may be wrong."
      >
        <Button asChild variant="outline">
          <ViewTransitionLink to="/admin/users" up>
            All users
          </ViewTransitionLink>
        </Button>
      </PageUnavailable>
    );
  }

  const account = user;
  const tab = parseUserDetailTab(searchParams.get("tab"));
  const viewAsDisabled = !canViewAsAccount(account, viewerId, viewerIsOwner);
  const manageable = canManageAccount(account, viewerId, viewerIsOwner);
  const policyManageable = canChangeAccessPolicy(account, viewerId, viewerIsOwner);
  const transferable =
    capabilities.data?.ownership_transfer === true &&
    canTransferOwnership(account, viewerId, viewerIsOwner);
  const ownAccount = account.id === viewerId;
  const groupName =
    account.access_group_id === null
      ? undefined
      : groups.find((group) => group.id === account.access_group_id)?.name;

  function selectTab(next: string) {
    setSearchParams(userDetailTabSearch(parseUserDetailTab(next)));
  }

  function handleTransfer() {
    setActionError("");
    transferOwnership.mutate(
      { id: account.id, profileContext: authority },
      {
        onSuccess: () => toast.success(`${account.username} is now the server owner`),
        onError: (err) =>
          setActionError(err instanceof Error ? err.message : "Could not transfer ownership."),
      },
    );
  }

  /** Reads the account fresh, so an action carries its current validator. */
  async function freshEditor(): Promise<AdminUserEditor | null> {
    if (busy.current || !available) return null;
    busy.current = true;
    setActionError("");
    try {
      return await getAdminUser(userId, authority);
    } catch (err) {
      setActionError(err instanceof Error ? err.message : "Could not load user.");
      return null;
    } finally {
      busy.current = false;
    }
  }

  async function setEnabled(enabled: boolean) {
    const fresh = await freshEditor();
    if (!fresh) return;
    busy.current = true;
    try {
      await updateUser.mutateAsync({ editor: fresh, body: { enabled } });
      toast.success(`${account.username} is ${enabled ? "enabled" : "disabled"}`);
    } catch (err) {
      if (err instanceof V2ProblemError && err.status === 412) {
        setActionError("The account changed. Try again.");
      } else {
        setActionError(err instanceof Error ? err.message : "Could not change the account.");
      }
    } finally {
      busy.current = false;
    }
  }

  async function openDelete() {
    const fresh = await freshEditor();
    if (fresh) setDeleteEditor(fresh);
  }

  return (
    <CardEditingProvider>
      <div className="page-shell min-w-0 space-y-6 py-4 sm:py-6">
        {actionError && <p role="alert">{actionError}</p>}
        {!available && <p role="status">User administration is unavailable.</p>}
        <UserDetailHeader
          user={account}
          groupName={groupName}
          manageable={manageable}
          available={available}
          viewAsDisabled={viewAsDisabled}
          transferable={transferable}
          ownAccount={ownAccount}
          onViewAs={() => setConfirmImpersonateOpen(true)}
          onResetPassword={() => setResetOpen(true)}
          onTransfer={() => setTransferOpen(true)}
          onDisable={() => setDisableOpen(true)}
          onEnable={() => void setEnabled(true)}
          onDelete={() => void openDelete()}
        />

        <Tabs value={tab} onValueChange={selectTab} className="min-w-0 gap-4">
          <UserDetailTabs user={account} active={tab} />
          <TabsContent value="overview" className="min-w-0">
            <OverviewTab user={account} />
          </TabsContent>
          <TabsContent value="access" className="min-w-0">
            <AccessTab
              user={account}
              editor={editor}
              manageable={manageable}
              policyManageable={policyManageable}
              available={available}
            />
          </TabsContent>
          <TabsContent value="activity" className="min-w-0">
            <ActivityTab user={account} />
          </TabsContent>
          <TabsContent value="downloads" className="min-w-0">
            <DownloadsTab user={account} />
          </TabsContent>
          <TabsContent value="preferences" className="min-w-0">
            <PreferencesTab user={account} />
          </TabsContent>
        </Tabs>

        <UnsavedCardGuard />
        <ConfirmDialog
          open={transferOpen}
          onOpenChange={setTransferOpen}
          title={`Make ${account.username} the server owner?`}
          description={`${account.username} becomes the only account that can manage admins, and you stay an admin. Only ${account.username} can transfer ownership back.`}
          confirmLabel="Make owner"
          onConfirm={handleTransfer}
          isPending={transferOwnership.isPending}
        />
        <ConfirmDialog
          open={disableOpen}
          onOpenChange={setDisableOpen}
          title={`Disable ${account.username}?`}
          description="They can't sign in until you enable the account again. Their profiles, history, and downloads are kept."
          confirmLabel="Disable account"
          variant="destructive"
          onConfirm={() => void setEnabled(false)}
        />
        {confirmImpersonateOpen && (
          <AdminUserImpersonationDialog
            user={account}
            returnPath={`/admin/users/${account.id}`}
            onClose={() => setConfirmImpersonateOpen(false)}
            onError={setActionError}
          />
        )}
        {resetOpen && (
          <AdminUserPasswordResetDialog
            user={account}
            emailAvailable={capabilities.data?.password_reset_email === true}
            linkAvailable={capabilities.data?.password_reset_link === true}
            profileContext={authority}
            onClose={() => setResetOpen(false)}
          />
        )}
        {deleteEditor && (
          <AdminUserDeleteDialog
            initialEditor={deleteEditor}
            profileCount={profiles.data?.length}
            onClose={() => setDeleteEditor(null)}
            onDeleted={() => navigate("/admin/users")}
            onDisabled={() => {
              setDeleteEditor(null);
              toast.success(`${account.username} is disabled`);
            }}
          />
        )}
      </div>
    </CardEditingProvider>
  );
}
