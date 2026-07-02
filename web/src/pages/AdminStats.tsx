import type { ReactNode } from "react";
import { useAdminStats, useAdminSessions } from "@/hooks/queries/admin/stats";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Skeleton } from "@/components/ui/skeleton";
import { Film, FileVideo, Users, Play } from "lucide-react";
import type { AdminSession, AdminStats } from "@/api/types";
import { getSessionClientLabel } from "@/pages/adminActivityPresentation";

export default function AdminStats() {
  const statsQuery = useAdminStats();
  const sessionsQuery = useAdminSessions();
  const sessions = sessionsQuery.data ?? [];

  return (
    <div className="page-shell space-y-6 py-4 sm:py-6">
      <div className="page-header gap-5">
        <div className="space-y-3">
          <h1 className="page-title text-[clamp(2rem,4vw,3rem)]">System stats</h1>
          <p className="page-subtitle text-sm sm:text-base">
            Track the size of the library and inspect the sessions currently playing through the
            system.
          </p>
        </div>
      </div>

      <StatsCards
        stats={statsQuery.data}
        sessionCount={sessions.length}
        isLoading={statsQuery.isLoading}
        error={statsQuery.error}
      />

      <SessionsSection
        sessions={sessions}
        isLoading={sessionsQuery.isLoading}
        error={sessionsQuery.error}
      />
    </div>
  );
}

function StatsCards({
  stats,
  sessionCount,
  isLoading,
  error,
}: {
  stats: AdminStats | undefined;
  sessionCount: number;
  isLoading: boolean;
  error: unknown;
}) {
  if (error) {
    return <SectionError message="Failed to load stats." />;
  }
  if (isLoading || !stats) {
    return (
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 md:grid-cols-4">
        {Array.from({ length: 4 }).map((_, i) => (
          <Skeleton key={i} className="h-[108px] rounded-2xl" />
        ))}
      </div>
    );
  }
  return (
    <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 md:grid-cols-4">
      <StatCard title="Media Items" value={stats.total_items} icon={<Film className="h-4 w-4" />} />
      <StatCard title="Files" value={stats.total_files} icon={<FileVideo className="h-4 w-4" />} />
      <StatCard title="Users" value={stats.total_users} icon={<Users className="h-4 w-4" />} />
      <StatCard title="Active Sessions" value={sessionCount} icon={<Play className="h-4 w-4" />} />
    </div>
  );
}

function SessionsSection({
  sessions,
  isLoading,
  error,
}: {
  sessions: AdminSession[];
  isLoading: boolean;
  error: unknown;
}) {
  return (
    <div className="space-y-3">
      <h2 className="text-lg font-medium tracking-tight">Active playback sessions</h2>
      {isLoading ? (
        <div className="surface-panel space-y-2 rounded-2xl p-4">
          {Array.from({ length: 3 }).map((_, i) => (
            <Skeleton key={i} className="h-10 w-full rounded-md" />
          ))}
        </div>
      ) : error ? (
        <SectionError message="Failed to load active sessions." />
      ) : sessions.length === 0 ? (
        <div className="surface-panel text-muted-foreground rounded-2xl py-12 text-center text-sm">
          No active sessions.
        </div>
      ) : (
        <div className="surface-panel overflow-x-auto rounded-2xl border-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Session ID</TableHead>
                <TableHead>User ID</TableHead>
                <TableHead>File ID</TableHead>
                <TableHead>Method</TableHead>
                <TableHead>Client</TableHead>
                <TableHead>Started</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {sessions.map((s) => (
                <TableRow key={s.session_id}>
                  <TableCell className="font-mono text-xs">{s.session_id.slice(0, 8)}...</TableCell>
                  <TableCell>{s.user_id}</TableCell>
                  <TableCell>{s.media_file_id}</TableCell>
                  <TableCell>{s.play_method}</TableCell>
                  <TableCell>{getSessionClientLabel(s) || "—"}</TableCell>
                  <TableCell className="text-xs">
                    {new Date(s.started_at).toLocaleString()}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}
    </div>
  );
}

function StatCard({ title, value, icon }: { title: string; value: number; icon: ReactNode }) {
  return (
    <Card className="surface-panel rounded-2xl border-0 shadow-none">
      <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
        <CardTitle className="text-muted-foreground text-sm font-medium">{title}</CardTitle>
        <div className="text-muted-foreground">{icon}</div>
      </CardHeader>
      <CardContent>
        <div className="text-3xl font-semibold tracking-tight">{value.toLocaleString()}</div>
      </CardContent>
    </Card>
  );
}

function SectionError({ message }: { message: string }) {
  return (
    <div className="surface-panel text-destructive rounded-2xl py-6 text-center text-sm">
      {message}
    </div>
  );
}
