import { Plus, UsersRound } from "lucide-react";
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { useLocation, useNavigate, useParams } from "react-router";
import { toast } from "sonner";

import { useAuth } from "@/hooks/useAuth";
import {
  accessGroupScope,
  captureAccessGroupAuthority,
  getAccessGroup,
  type AccessGroupEditor as GroupEditor,
} from "@/api/v2/accessGroups";
import type { AccessGroup } from "@/api/types";
import { AccessGroupEditor } from "@/components/admin/access-groups/AccessGroupEditor";
import { PageHeader } from "@/components/page/PageHeader";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  useAccessGroupCapabilities,
  useAccessGroups,
  useCreateAccessGroup,
} from "@/hooks/queries/admin/accessGroups";
import { useRequestGroupLimit } from "@/hooks/queries/admin/requests";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";
import { useT, type MessageKey } from "@/i18n";
import { requestGroupLimitSummary } from "@/lib/requestAccess";
import { TOUCH_TARGETS } from "@/lib/touchTargets";
import { cn } from "@/lib/utils";

export default function AdminAccessGroups() {
  useAuth();
  return <AccessGroupsPage key={accessGroupScope()} />;
}
function AccessGroupsPage() {
  const t = useT();
  useDocumentTitle(t("web.admin.access.groups.title"));
  const groups = useAccessGroups();
  const capabilities = useAccessGroupCapabilities();
  const available = capabilities.data?.access_groups === true;
  // The open group lives in the URL (/admin/access-groups/:id) so Back returns
  // to the list and a group link can be reloaded or shared.
  const { id: selectedId } = useParams();
  const navigate = useNavigate();
  const [selected, setSelected] = useState<GroupEditor | null>(null);
  const [authority] = useState(captureAccessGroupAuthority);
  const busy = useRef(false);
  // The failure and the message to show when it carries none; text is resolved
  // at render so the load effect does not depend on the language.
  const [error, setError] = useState<{ cause: unknown; fallback: MessageKey } | null>(null);
  const errorText = error
    ? error.cause instanceof Error
      ? error.cause.message
      : t(error.fallback)
    : "";
  const [loading, setLoading] = useState(false);
  const [creating, setCreating] = useState(false);
  const [newName, setNewName] = useState("");
  // Bumped to retry a failed load of the group already in the URL.
  const [loadAttempt, setLoadAttempt] = useState(0);
  const mounted = useRef(true);
  // One page instance serves the list and every group URL, so a finished create
  // or delete compares history entries to tell whether the admin moved on meanwhile.
  const location = useLocation();
  const locationKey = useRef(location.key);
  const createGroup = useCreateAccessGroup();

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  useLayoutEffect(() => {
    locationKey.current = location.key;
  }, [location.key]);

  function stillOn(key: string) {
    return mounted.current && locationKey.current === key;
  }

  useEffect(() => {
    setSelected(null);
    if (!selectedId || !available) {
      // A load cancelled by leaving the group must not leave its status behind.
      setLoading(false);
      setError(null);
      return;
    }
    let cancelled = false;
    setLoading(true);
    setError(null);
    getAccessGroup(Number(selectedId), authority)
      .then((editor) => {
        if (!cancelled) setSelected(editor);
      })
      .catch((err: unknown) => {
        if (!cancelled)
          setError({ cause: err, fallback: "web.admin.access.groups.groupLoadFailed" });
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [selectedId, available, authority, loadAttempt]);

  function select(id: number | string) {
    if (!available) return;
    if (String(id) === selectedId) {
      setLoadAttempt((attempt) => attempt + 1);
      return;
    }
    navigate(`/admin/access-groups/${id}`);
  }
  async function create() {
    const name = newName.trim();
    if (!name || busy.current || !available) return;
    busy.current = true;
    const startedAt = locationKey.current;
    setError(null);
    try {
      const group = await createGroup.mutateAsync({ body: { name }, profileContext: authority });
      setNewName("");
      setCreating(false);
      // Don't pull the admin away if they opened another group or left the page
      // while the group was created.
      if (stillOn(startedAt)) select(group.id);
    } catch (err) {
      setError({ cause: err, fallback: "web.admin.access.groups.createFailed" });
    } finally {
      busy.current = false;
    }
  }

  if (selected && available) {
    return (
      <div className={cn("space-y-6 pb-4 sm:pb-6", TOUCH_TARGETS)}>
        <AccessGroupEditor
          key={selected.group.id}
          initialEditor={selected}
          onBack={() => navigate("/admin/access-groups")}
          onSaved={() => {
            toast.success(t("web.admin.access.groups.saved"));
            // Replace the group's entry so Back and "All groups" reach the same list.
            if (stillOn(location.key)) navigate("/admin/access-groups", { replace: true });
          }}
          onDeleted={() => {
            if (stillOn(location.key)) navigate("/admin/access-groups", { replace: true });
          }}
        />
      </div>
    );
  }

  return (
    <div className={cn("space-y-6 pb-4 sm:pb-6", TOUCH_TARGETS)}>
      <PageHeader
        title={t("web.admin.access.groups.title")}
        subtitle={t("web.admin.access.groups.subtitle")}
        // On a phone the New group button takes a row of its own, full width.
        className="max-lg:[&>div:last-child]:w-full"
        actions={
          !creating &&
          available && (
            <Button type="button" className="max-lg:flex-1" onClick={() => setCreating(true)}>
              <Plus className="size-4" />
              {t("web.admin.access.groups.newGroup")}
            </Button>
          )
        }
      />

      {!available && <p role="status">{t("web.admin.access.groups.unavailable")}</p>}
      {errorText && <p role="alert">{errorText}</p>}
      {loading && <p>{t("web.admin.access.groups.loadingEditor")}</p>}
      {groups.isError && (
        <div role="alert">
          {t("web.admin.access.groups.loadFailed", {
            reload: (chunks) => <Button onClick={() => void groups.refetch()}>{chunks}</Button>,
          })}
        </div>
      )}
      {creating && available && (
        <div className="surface-panel-subtle flex flex-wrap items-center gap-2 rounded-2xl p-4">
          <Input
            value={newName}
            onChange={(event) => setNewName(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === "Enter") void create();
            }}
            placeholder={t("web.admin.access.groups.create.placeholder")}
            aria-label={t("web.admin.access.groups.create.nameLabel")}
            className="max-lg:basis-full lg:max-w-xs"
            autoFocus
          />
          <Button
            type="button"
            className="max-lg:flex-1"
            onClick={create}
            disabled={createGroup.isPending}
          >
            {t("web.admin.access.groups.create.submit")}
          </Button>
          <Button
            type="button"
            variant="ghost"
            disabled={createGroup.isPending}
            onClick={() => {
              setCreating(false);
              setNewName("");
            }}
          >
            {t("common.cancel")}
          </Button>
        </div>
      )}

      {groups.isLoading && (
        <p className="text-muted-foreground text-sm">{t("web.admin.access.groups.loadingList")}</p>
      )}

      {!groups.isLoading && groups.data?.length === 0 && !creating && (
        <div className="surface-panel-subtle rounded-2xl p-8 text-center">
          <UsersRound className="text-muted-foreground mx-auto size-8" />
          <h2 className="mt-3 text-lg font-semibold">{t("web.admin.access.groups.empty.title")}</h2>
          <p className="text-muted-foreground mx-auto mt-1 max-w-md text-sm">
            {t("web.admin.access.groups.empty.body")}
          </p>
        </div>
      )}

      {groups.data && groups.data.length > 0 && (
        <div className="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-3">
          {groups.data.map((group) => (
            <AccessGroupCard key={group.id} group={group} onClick={() => select(group.id)} />
          ))}
        </div>
      )}
    </div>
  );
}

function AccessGroupCard({ group, onClick }: { group: AccessGroup; onClick: () => void }) {
  const t = useT();
  const requestLimit = useRequestGroupLimit(Number(group.id));
  // The group's own request approval and limit, when it sets any; a group
  // that only follows the server-wide defaults says nothing more.
  const requestTerms =
    group.requests_allowed && requestLimit.data ? requestGroupLimitSummary(requestLimit.data) : "";
  const facts = [
    group.library_ids === null
      ? t("web.admin.access.groups.card.allLibraries")
      : t("web.admin.access.groups.card.libraryCount", {
          count: group.library_ids.length,
          count_text: group.library_ids.length,
        }),
    group.download_allowed
      ? t("web.admin.access.groups.card.downloadsOn")
      : t("web.admin.access.groups.card.downloadsOff"),
    t("web.admin.access.groups.card.streams", {
      count: Math.max(group.max_streams, 0),
      count_text: Math.max(group.max_streams, 0),
    }),
    group.requests_allowed
      ? t("web.admin.access.groups.card.requestsOn")
      : t("web.admin.access.groups.card.requestsOff"),
    ...(requestTerms ? [requestTerms] : []),
  ];
  return (
    <button
      type="button"
      onClick={onClick}
      className="surface-panel hover:border-ring/40 focus-visible:ring-ring/60 flex min-w-0 flex-col gap-3 rounded-2xl border border-transparent p-5 text-start transition-colors outline-none focus-visible:ring-2"
    >
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <h2 className="truncate text-base font-semibold tracking-tight max-lg:line-clamp-2 max-lg:whitespace-normal">
              {group.name}
            </h2>
            {group.is_default && (
              <span className="border-border text-muted-foreground shrink-0 rounded-full border px-2 py-0.5 text-[10px] font-medium tracking-wide uppercase">
                {t("web.admin.access.groups.card.default")}
              </span>
            )}
          </div>
          {group.description && (
            <p className="text-muted-foreground mt-0.5 line-clamp-2 text-sm">{group.description}</p>
          )}
        </div>
        <span className="bg-secondary text-secondary-foreground shrink-0 rounded-full px-2.5 py-0.5 text-xs font-medium">
          {t("web.admin.access.groups.card.memberCount", {
            count: group.member_count,
            count_text: group.member_count,
          })}
        </span>
      </div>
      <div className="flex flex-wrap gap-1.5">
        {facts.map((fact) => (
          <span
            key={fact}
            className="border-border text-muted-foreground rounded-md border px-2 py-0.5 text-xs"
          >
            {fact}
          </span>
        ))}
      </div>
    </button>
  );
}
