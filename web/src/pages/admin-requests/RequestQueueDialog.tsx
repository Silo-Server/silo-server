import { useMemo } from "react";
import type { ReactNode } from "react";
import { Link } from "react-router";
import { AlertTriangle, ExternalLink, Library, X } from "lucide-react";
import type { MediaRequest } from "@/api/types";
import { RequestDownloadProgress } from "@/components/RequestDownloadProgress";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { useAdminPluginInstallations } from "@/hooks/queries/admin/plugins";
import { useAdminUsers } from "@/hooks/queries/admin/users";
import {
  useAdminRequestEvents,
  useAdminRequestRoutePreview,
  useRequestIntegrations,
  useRequestRoutes,
} from "@/hooks/queries/admin/requests";
import { formatRelativeTime } from "@/lib/date";
import { formatDateTime } from "@/lib/datetime";
import {
  formatMediaType,
  formatSeasonList,
  requestDetailHref,
  tmdbImageURL,
} from "@/lib/mediaRequests";
import { RoutePreviewResult } from "@/pages/admin-settings/RequestRoutePreview";
import { requestRouterInstallations } from "@/pages/admin-settings/requestServerModel";
import { RequestActionButtons, type RequestQueueActionHandlers } from "./RequestActionButtons";
import { RequestPoster, RequestStateSummary, TargetStatusBadge } from "./queueParts";
import { formatRequestEventType, requestQueueView, targetServerName } from "./requestQueueModel";

/** Everything about one request: its servers, where it would go, and its history. */
export function RequestQueueDialog({
  request,
  requesterName,
  handlers,
  onOpenChange,
}: {
  request: MediaRequest | null;
  requesterName?: string;
  handlers: RequestQueueActionHandlers;
  onOpenChange: (open: boolean) => void;
}) {
  return (
    <Dialog open={request !== null} onOpenChange={onOpenChange}>
      <DialogContent
        className="flex max-h-[min(46rem,calc(100dvh-4rem))] flex-col gap-0 overflow-hidden p-0 sm:max-w-2xl"
        showCloseButton={false}
      >
        {/* Its own close button, with a backing that stays visible over the backdrop. */}
        <DialogClose className="bg-background/75 hover:bg-background focus-visible:ring-ring absolute top-3 right-3 z-10 rounded-md p-1.5 backdrop-blur transition-colors focus-visible:ring-2 focus-visible:outline-none">
          <X className="size-4" aria-hidden="true" />
          <span className="sr-only">Close</span>
        </DialogClose>
        {request ? (
          <RequestDetail request={request} requesterName={requesterName} handlers={handlers} />
        ) : null}
      </DialogContent>
    </Dialog>
  );
}

function RequestDetail({
  request,
  requesterName,
  handlers,
}: {
  request: MediaRequest;
  requesterName?: string;
  handlers: RequestQueueActionHandlers;
}) {
  const backdrop = tmdbImageURL(request.backdrop_path, "w780");
  const view = requestQueueView(request);
  const requester =
    requesterName ??
    (request.requested_by_user_id ? `User ${request.requested_by_user_id}` : "Unknown account");
  const requestedAgo = formatRelativeTime(request.created_at, { absoluteAfterDays: 30 });
  return (
    <>
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="relative">
          {backdrop ? (
            <div className="relative h-36 overflow-hidden sm:h-44">
              <img src={backdrop} alt="" className="h-full w-full object-cover" />
              <div className="from-background absolute inset-0 bg-gradient-to-t to-transparent" />
            </div>
          ) : (
            <div className="h-10" />
          )}
          <DialogHeader
            className={
              backdrop
                ? "-mt-16 flex-row items-end gap-4 px-6 text-left"
                : "flex-row gap-4 px-6 pt-6 text-left"
            }
          >
            <RequestPoster request={request} size="w185" className="relative w-20 shadow-md" />
            <div className="min-w-0 space-y-1 pr-6">
              <DialogTitle className="text-lg leading-tight text-balance">
                <Link
                  to={requestDetailHref(request.media_type, request.tmdb_id)}
                  className="hover:underline"
                >
                  {request.title}
                </Link>
              </DialogTitle>
              <DialogDescription>
                {[request.year, formatMediaType(request.media_type)].filter(Boolean).join(" · ")}
              </DialogDescription>
            </div>
          </DialogHeader>
        </div>

        <div className="space-y-6 px-6 pt-5 pb-6">
          <Section title="Request">
            <dl className="grid grid-cols-[8rem_minmax(0,1fr)] gap-x-3 gap-y-2 text-sm">
              <Term label="Requested by">
                {request.requested_by_user_id ? (
                  <Link
                    to={`/admin/users/${request.requested_by_user_id}`}
                    className="hover:underline"
                  >
                    {requester}
                  </Link>
                ) : (
                  requester
                )}
              </Term>
              <Term label="Requested">
                {formatDateTime(request.created_at)}
                {requestedAgo ? (
                  <span className="text-muted-foreground"> · {requestedAgo}</span>
                ) : null}
              </Term>
              {request.approved_at ? (
                <Term label="Approved">{formatDateTime(request.approved_at)}</Term>
              ) : null}
              {request.completed_at ? (
                <Term label="Completed">{formatDateTime(request.completed_at)}</Term>
              ) : null}
              {request.media_type === "series" ? (
                <Term label="Seasons">
                  {request.seasons?.length ? formatSeasonList(request.seasons) : "Whole series"}
                  {request.season_progress?.length ? (
                    <ul className="text-muted-foreground mt-1 space-y-0.5 text-xs">
                      {request.season_progress.map((season) => (
                        <li key={season.season_number}>
                          Season {season.season_number}: {season.episodes_available}
                          {season.episodes_aired > 0 ? ` of ${season.episodes_aired}` : ""}{" "}
                          {season.episodes_available === 1 ? "episode" : "episodes"} in the library
                        </li>
                      ))}
                    </ul>
                  ) : null}
                </Term>
              ) : null}
              <Term label="State">
                <RequestStateSummary request={request} />
              </Term>
              <Term label="TMDB ID">{request.tmdb_id}</Term>
              {request.library_content_id ? (
                <Term label="Library">
                  <Link
                    to={`/item/${encodeURIComponent(request.library_content_id)}`}
                    className="inline-flex items-center gap-1 hover:underline"
                  >
                    <Library className="h-3.5 w-3.5" aria-hidden="true" />
                    Open in the library
                  </Link>
                </Term>
              ) : null}
            </dl>
          </Section>

          {request.targets?.length ? (
            <Section title="Servers">
              <TargetsTable request={request} />
            </Section>
          ) : null}

          {view === "needs_approval" || view === "failed" ? (
            <Section title="Where it would go">
              <RoutePreviewSection request={request} />
            </Section>
          ) : null}

          <Section title="History">
            <RequestHistory requestId={request.id} />
          </Section>
        </div>
      </div>

      <DialogFooter className="border-border shrink-0 flex-row flex-wrap items-center justify-between gap-2 border-t px-6 py-3 sm:justify-between">
        <RequestActionButtons request={request} handlers={handlers} />
        <Button asChild variant="ghost" size="sm">
          <Link to={requestDetailHref(request.media_type, request.tmdb_id)}>
            <ExternalLink aria-hidden="true" />
            Title page
          </Link>
        </Button>
      </DialogFooter>
    </>
  );
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="space-y-2">
      <h3 className="text-muted-foreground text-xs font-semibold tracking-wide uppercase">
        {title}
      </h3>
      {children}
    </section>
  );
}

function Term({ label, children }: { label: string; children: ReactNode }) {
  return (
    <>
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="min-w-0">{children}</dd>
    </>
  );
}

function TargetsTable({ request }: { request: MediaRequest }) {
  return (
    <div className="border-border overflow-x-auto rounded-lg border">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>Quality</TableHead>
            <TableHead>Server</TableHead>
            <TableHead>Route</TableHead>
            <TableHead>Status</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {request.targets?.map((target) => (
            <TargetRows key={target.id} target={target} />
          ))}
        </TableBody>
      </Table>
    </div>
  );
}

function TargetRows({ target }: { target: NonNullable<MediaRequest["targets"]>[number] }) {
  return (
    <>
      <TableRow className={target.last_error ? "border-b-0" : undefined}>
        <TableCell className="font-medium">{target.quality}</TableCell>
        <TableCell>{targetServerName(target)}</TableCell>
        <TableCell className="text-muted-foreground">{target.route_name || "—"}</TableCell>
        <TableCell>
          <div className="flex flex-col items-start gap-0.5">
            <TargetStatusBadge target={target} />
            {target.external_status ? (
              <span className="text-muted-foreground text-xs">{target.external_status}</span>
            ) : null}
            {target.download ? (
              <RequestDownloadProgress
                download={target.download}
                admin
                className="mt-1 w-full min-w-32"
              />
            ) : null}
          </div>
        </TableCell>
      </TableRow>
      {target.last_error ? (
        <TableRow>
          <TableCell colSpan={4} className="pt-0 whitespace-normal">
            <p className="text-destructive flex items-start gap-1.5 text-xs">
              <AlertTriangle className="mt-0.5 h-3 w-3 shrink-0" aria-hidden="true" />
              <span className="break-words">{target.last_error}</span>
            </p>
          </TableCell>
        </TableRow>
      ) : null}
    </>
  );
}

function RoutePreviewSection({ request }: { request: MediaRequest }) {
  const preview = useAdminRequestRoutePreview({
    mediaType: request.media_type,
    tmdbId: request.tmdb_id,
    requesterUserId: request.requested_by_user_id,
  });
  // Server and plugin names turn override IDs into the names the settings use.
  const servers = useRequestIntegrations();
  // The routes and accounts turn the explanation's rule and account IDs into names.
  const routes = useRequestRoutes();
  const users = useAdminUsers();
  const names = useMemo(
    () => ({ users: new Map((users.data ?? []).map((user) => [user.id, user.username])) }),
    [users.data],
  );
  const installationsQuery = useAdminPluginInstallations();
  const installations = useMemo(
    () => requestRouterInstallations(installationsQuery.data ?? []),
    [installationsQuery.data],
  );
  return (
    <div
      aria-live="polite"
      className="border-border/70 bg-foreground/[0.02] space-y-2 rounded-xl border p-3 text-sm"
    >
      {preview.isPending ? (
        <p className="text-muted-foreground text-xs">Checking the routing rules…</p>
      ) : preview.isError ? (
        <p className="text-destructive text-xs">
          {preview.error instanceof Error ? preview.error.message : "The preview failed."}
        </p>
      ) : (
        <RoutePreviewResult
          preview={preview.data}
          mediaType={request.media_type}
          servers={servers.data ?? []}
          installations={installations}
          routes={routes.data ?? []}
          names={names}
          requesterUserId={request.requested_by_user_id}
          traceOpen={false}
        />
      )}
      <p className="text-muted-foreground text-xs">
        With routing as saved now.{" "}
        <Link to="/admin/settings/requests" className="hover:text-foreground underline">
          Routing settings
        </Link>
      </p>
    </div>
  );
}

function RequestHistory({ requestId }: { requestId: string }) {
  const events = useAdminRequestEvents(requestId);
  if (events.isPending) return <p className="text-muted-foreground text-sm">Loading history…</p>;
  if (events.isError) {
    return (
      <p className="text-destructive text-sm">
        {events.error instanceof Error ? events.error.message : "History could not be loaded."}
      </p>
    );
  }
  if (events.data.length === 0) {
    return <p className="text-muted-foreground text-sm">No history recorded.</p>;
  }
  return (
    <ol aria-label="Request history" className="border-border space-y-3 border-l pl-4">
      {events.data.map((event) => (
        <li key={event.id} className="relative text-sm">
          <span
            aria-hidden="true"
            className="bg-muted-foreground/60 absolute top-1.5 -left-[1.3rem] h-2 w-2 rounded-full"
          />
          <div className="flex flex-wrap items-baseline gap-x-2">
            <span className="font-medium">{formatRequestEventType(event.type)}</span>{" "}
            {event.actor_username || event.actor_user_id ? (
              <span className="text-muted-foreground text-xs">
                by {event.actor_username || `User ${event.actor_user_id}`}
              </span>
            ) : null}{" "}
            <time
              dateTime={event.created_at}
              title={formatDateTime(event.created_at)}
              className="text-muted-foreground text-xs"
            >
              {formatRelativeTime(event.created_at, { absoluteAfterDays: 30 }) ??
                formatDateTime(event.created_at)}
            </time>
          </div>
          {event.message ? (
            <p className="text-muted-foreground mt-0.5 text-xs break-words">{event.message}</p>
          ) : null}
        </li>
      ))}
    </ol>
  );
}
