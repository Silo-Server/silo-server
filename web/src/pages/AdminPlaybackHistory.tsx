import { useCallback, useEffect, useRef, useState } from "react";
import { Link, useSearchParams } from "react-router";
import { useAdminUsers } from "@/hooks/queries/admin/users";
import { useAdminPlaybackHistory, useAdminUserProfiles } from "@/hooks/queries/admin/history";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { History, RefreshCw } from "lucide-react";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Skeleton } from "@/components/ui/skeleton";

const ALL_USERS = "all";
const ALL_PROFILES = "all";
const ALL_COMPLETION = "all";
const PAGE_SIZE_OPTIONS = ["25", "50", "100"] as const;
const REFRESH_SPINNER_MIN_VISIBLE_MS = 1_000;

export default function AdminPlaybackHistory() {
  const [searchParams, setSearchParams] = useSearchParams();
  const { data: users = [] } = useAdminUsers();
  const [page, setPage] = useState(0);
  const [pageSize, setPageSize] = useState(25);
  const manualRefreshStartedAtRef = useRef<number | null>(null);
  const [isManualRefreshPending, setIsManualRefreshPending] = useState(false);

  const selectedUser = searchParams.get("user_id") ?? ALL_USERS;
  const selectedProfile = searchParams.get("profile_id") ?? ALL_PROFILES;
  const selectedCompleted = searchParams.get("completed") ?? ALL_COMPLETION;
  const selectedMediaItemId = searchParams.get("media_item_id")?.trim() ?? "";

  const selectedUserId = selectedUser !== ALL_USERS ? Number(selectedUser) : undefined;
  const history = useAdminPlaybackHistory({
    userId: selectedUserId,
    profileId: selectedProfile !== ALL_PROFILES ? selectedProfile : undefined,
    mediaItemId: selectedMediaItemId || undefined,
    completed:
      selectedCompleted === "true" || selectedCompleted === "false" ? selectedCompleted : "all",
    limit: 100,
  });
  const profiles = useAdminUserProfiles(selectedUserId);
  const { refetch: refetchHistory } = history;

  const refreshHistory = useCallback(async () => {
    manualRefreshStartedAtRef.current = Date.now();
    setIsManualRefreshPending(true);
    try {
      await refetchHistory();
    } finally {
      const startedAt = manualRefreshStartedAtRef.current;
      if (startedAt !== null) {
        const elapsed = Date.now() - startedAt;
        const remaining = REFRESH_SPINNER_MIN_VISIBLE_MS - elapsed;
        if (remaining > 0) {
          await delay(remaining);
        }
      }
      manualRefreshStartedAtRef.current = null;
      setIsManualRefreshPending(false);
    }
  }, [refetchHistory]);

  useEffect(() => {
    if (selectedUser === ALL_USERS && selectedProfile !== ALL_PROFILES) {
      const next = new URLSearchParams(searchParams);
      next.delete("profile_id");
      setSearchParams(next, { replace: true });
      return;
    }
    if (
      selectedUser !== ALL_USERS &&
      selectedProfile !== ALL_PROFILES &&
      profiles.data &&
      profiles.data.length > 0 &&
      !profiles.data.some((profile) => profile.id === selectedProfile)
    ) {
      const next = new URLSearchParams(searchParams);
      next.delete("profile_id");
      setSearchParams(next, { replace: true });
    }
  }, [profiles.data, searchParams, selectedProfile, selectedUser, setSearchParams]);

  function updateFilter(key: string, value: string) {
    const next = new URLSearchParams(searchParams);
    if (value === ALL_USERS || value === ALL_PROFILES || value === ALL_COMPLETION) {
      next.delete(key);
    } else {
      next.set(key, value);
    }
    if (key === "user_id") {
      next.delete("profile_id");
    }
    setPage(0);
    setSearchParams(next, { replace: true });
  }

  function resetFilters() {
    setPage(0);
    setSearchParams(new URLSearchParams(), { replace: true });
  }

  const allRows = history.data ?? [];
  const total = allRows.length;
  const paginatedRows = allRows.slice(page * pageSize, (page + 1) * pageSize);
  const activeMediaItemLabel = allRows[0]?.media_title || selectedMediaItemId;

  return (
    <div className="page-shell space-y-6 py-4 sm:py-6">
      <div className="page-header gap-5">
        <div className="space-y-3">
          <h1 className="page-title text-[clamp(2rem,4vw,3rem)]">Playback History</h1>
          <p className="page-subtitle text-sm sm:text-base">
            Finalized playback attempts across all users and profiles.
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {selectedMediaItemId && (
            <div className="border-border bg-muted/40 flex items-center gap-2 rounded-md border px-3 py-2 text-xs">
              <span className="text-muted-foreground font-medium">Item filter active</span>
              <Link
                to={`/item/${encodeURIComponent(selectedMediaItemId)}`}
                className="hover:text-primary font-semibold transition-colors hover:underline"
              >
                {activeMediaItemLabel}
              </Link>
            </div>
          )}

          <Select value={selectedUser} onValueChange={(value) => updateFilter("user_id", value)}>
            <SelectTrigger className="w-[220px]">
              <SelectValue placeholder="All users" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL_USERS}>All users</SelectItem>
              {users.map((user) => (
                <SelectItem key={user.id} value={String(user.id)}>
                  {user.username}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>

          <Select
            value={selectedProfile}
            onValueChange={(value) => updateFilter("profile_id", value)}
            disabled={selectedUser === ALL_USERS}
          >
            <SelectTrigger className="w-[220px]">
              <SelectValue
                placeholder={selectedUser === ALL_USERS ? "Choose a user first" : "All profiles"}
              />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL_PROFILES}>All profiles</SelectItem>
              {(profiles.data ?? []).map((profile) => (
                <SelectItem key={profile.id} value={profile.id}>
                  {profile.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>

          <Select
            value={selectedCompleted}
            onValueChange={(value) => updateFilter("completed", value)}
          >
            <SelectTrigger className="w-[180px]">
              <SelectValue placeholder="All attempts" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL_COMPLETION}>All attempts</SelectItem>
              <SelectItem value="true">Completed</SelectItem>
              <SelectItem value="false">Partial</SelectItem>
            </SelectContent>
          </Select>

          <Button variant="outline" size="sm" onClick={resetFilters}>
            Reset
          </Button>
          <Button
            variant="outline"
            size="sm"
            className="min-w-[8.25rem] justify-center"
            onClick={() => {
              void refreshHistory();
            }}
            disabled={isManualRefreshPending}
            aria-busy={isManualRefreshPending}
          >
            <RefreshCw className={`h-3.5 w-3.5 ${isManualRefreshPending ? "animate-spin" : ""}`} />
            {isManualRefreshPending ? "Refreshing..." : "Refresh"}
          </Button>
        </div>
      </div>

      <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
        <InfoCard label="Visible Rows" value={String(allRows.length)} />
        <InfoCard label="Completed" value={String(allRows.filter((row) => row.completed).length)} />
        <InfoCard label="Partial" value={String(allRows.filter((row) => !row.completed).length)} />
      </div>

      <Card className="surface-panel rounded-2xl border-0">
        <CardHeader className="flex flex-row items-center justify-between space-y-0">
          <CardTitle className="text-sm font-bold">Recent Playback</CardTitle>
          <div className="text-muted-foreground text-xs">
            {history.isFetching ? "Refreshing..." : "Auto-refreshing"}
          </div>
        </CardHeader>
        <CardContent>
          {history.isLoading ? (
            <div className="space-y-3">
              <Skeleton className="h-10 w-full rounded-lg" />
              {Array.from({ length: 5 }).map((_, i) => (
                <Skeleton key={i} className="h-12 w-full rounded-lg" />
              ))}
            </div>
          ) : history.error ? (
            <div className="text-destructive py-8 text-center text-sm">
              {history.error instanceof Error
                ? history.error.message
                : "Failed to load playback history"}
            </div>
          ) : allRows.length === 0 ? (
            <div className="flex flex-col items-center justify-center gap-3 py-12 text-center">
              <History className="text-muted-foreground/50 h-10 w-10" />
              <div className="space-y-1">
                <p className="text-sm font-medium">No playback history</p>
                <p className="text-muted-foreground max-w-sm text-xs">
                  No playback history matches the current filters. Try adjusting the user, profile,
                  or completion filters.
                </p>
              </div>
            </div>
          ) : (
            <>
              <div className="overflow-x-auto">
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>Media</TableHead>
                      <TableHead>User</TableHead>
                      <TableHead>Profile</TableHead>
                      <TableHead>Method</TableHead>
                      <TableHead>Watch Time</TableHead>
                      <TableHead>Status</TableHead>
                      <TableHead>Ended</TableHead>
                      <TableHead className="text-right">Logs</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {paginatedRows.map((row) => {
                      const title =
                        row.media_title || row.media_item_id || `File #${row.media_file_id}`;
                      const profileLabel = row.profile_name || row.profile_id;
                      return (
                        <TableRow key={row.session_id}>
                          <TableCell>
                            <div className="space-y-1">
                              {row.media_item_id ? (
                                <Link
                                  to={`/item/${encodeURIComponent(row.media_item_id)}`}
                                  className="hover:text-primary block font-medium transition-colors hover:underline"
                                >
                                  {title}
                                </Link>
                              ) : (
                                <div className="font-medium">{title}</div>
                              )}
                              <div className="text-muted-foreground text-xs">
                                {row.media_type || "unknown"} · session {row.session_id.slice(0, 8)}
                              </div>
                            </div>
                          </TableCell>
                          <TableCell>
                            <Link
                              to={`/admin/users/${row.user_id}`}
                              className="hover:text-primary font-medium transition-colors hover:underline"
                            >
                              {row.username || `User #${row.user_id}`}
                            </Link>
                          </TableCell>
                          <TableCell>
                            <div className="space-y-1">
                              <Link
                                to={`/admin/history?user_id=${row.user_id}&profile_id=${encodeURIComponent(row.profile_id)}`}
                                className="hover:text-primary block transition-colors hover:underline"
                              >
                                {profileLabel}
                              </Link>
                              <div className="text-muted-foreground text-xs">{row.profile_id}</div>
                            </div>
                          </TableCell>
                          <TableCell>
                            <Badge variant="secondary">{row.play_method}</Badge>
                          </TableCell>
                          <TableCell>
                            <div className="space-y-1">
                              <div>{formatDuration(row.watched_seconds)}</div>
                              <div className="text-muted-foreground text-xs">
                                of {formatDuration(row.duration_seconds)}
                              </div>
                            </div>
                          </TableCell>
                          <TableCell>
                            <Badge variant={row.completed ? "default" : "outline"}>
                              {row.completed ? "Completed" : "Partial"}
                            </Badge>
                          </TableCell>
                          <TableCell>
                            <div className="space-y-1">
                              <div>{formatDateTime(row.ended_at)}</div>
                              <div className="text-muted-foreground text-xs">
                                started {formatRelative(row.started_at)}
                              </div>
                            </div>
                          </TableCell>
                          <TableCell className="text-right">
                            <div className="flex justify-end gap-3">
                              <Link
                                to={`/admin/logs?playback_session_id=${encodeURIComponent(row.session_id)}&focus=playback`}
                                className="text-primary text-sm font-medium"
                              >
                                View Logs
                              </Link>
                              <Link
                                to={`/admin/logs?playback_session_id=${encodeURIComponent(row.session_id)}&focus=playback&component=ffmpeg`}
                                className="text-primary/80 text-sm font-medium"
                              >
                                FFmpeg Logs
                              </Link>
                            </div>
                          </TableCell>
                        </TableRow>
                      );
                    })}
                  </TableBody>
                </Table>
              </div>
              {total > 0 && (
                <div className="flex items-center justify-between px-2 py-4">
                  <div className="flex items-center gap-4">
                    <span className="text-muted-foreground text-sm">
                      Showing {page * pageSize + 1}-{Math.min((page + 1) * pageSize, total)} of{" "}
                      {total}
                    </span>
                    <Select
                      value={String(pageSize)}
                      onValueChange={(v) => {
                        setPageSize(Number(v));
                        setPage(0);
                      }}
                    >
                      <SelectTrigger className="h-8 w-[100px]">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {PAGE_SIZE_OPTIONS.map((size) => (
                          <SelectItem key={size} value={size}>
                            {size} rows
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  </div>
                  <div className="flex gap-2">
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={() => setPage((p) => p - 1)}
                      disabled={page === 0}
                    >
                      Previous
                    </Button>
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={() => setPage((p) => p + 1)}
                      disabled={(page + 1) * pageSize >= total}
                    >
                      Next
                    </Button>
                  </div>
                </div>
              )}
            </>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

function InfoCard({ label, value }: { label: string; value: string }) {
  return (
    <div className="surface-panel rounded-2xl border-0 p-4">
      <div className="text-muted-foreground text-[11px] font-medium">{label}</div>
      <div className="mt-1 text-2xl font-extrabold tracking-tight">{value}</div>
    </div>
  );
}

function formatDuration(seconds: number | null) {
  if (!seconds || Number.isNaN(seconds)) return "0m";
  const rounded = Math.max(0, Math.floor(seconds));
  const hours = Math.floor(rounded / 3600);
  const minutes = Math.floor((rounded % 3600) / 60);
  const secs = rounded % 60;

  if (hours > 0) return `${hours}h ${minutes}m`;
  if (minutes > 0) return `${minutes}m ${secs}s`;
  return `${secs}s`;
}

function formatDateTime(value: string) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString();
}

function formatRelative(value: string) {
  const date = new Date(value);
  const diff = Date.now() - date.getTime();
  if (Number.isNaN(date.getTime())) return value;
  const minutes = Math.max(0, Math.floor(diff / 60_000));
  if (minutes < 1) return "just now";
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.floor(hours / 24);
  return `${days}d ago`;
}

function delay(ms: number) {
  return new Promise<void>((resolve) => {
    window.setTimeout(resolve, ms);
  });
}
