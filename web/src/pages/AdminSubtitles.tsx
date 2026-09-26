import { useState } from "react";
import { adminSubtitleListScope } from "@/api/v2/adminSubtitles";
import { useSearchParams } from "react-router";
import AdminSubtitlesFilters, {
  FILTER_ALL,
} from "@/components/admin/subtitles/AdminSubtitlesFilters";
import AdminSubtitlesTable from "@/components/admin/subtitles/AdminSubtitlesTable";
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { useAdminDownloadedSubtitles } from "@/hooks/queries/admin/subtitles";
import { useAdminUsers } from "@/hooks/queries/admin/users";

const PAGE_SIZE_OPTIONS = ["25", "50", "100"] as const;

export default function AdminSubtitles() {
  const [searchParams, setSearchParams] = useSearchParams();
  const { data: users = [] } = useAdminUsers();

  const [pageSize, setPageSize] = useState(25);

  const provider = searchParams.get("provider") ?? FILTER_ALL;
  const language = searchParams.get("language") ?? FILTER_ALL;
  const userId = searchParams.get("user_id") ?? FILTER_ALL;
  const search = searchParams.get("q") ?? "";

  const scope = JSON.stringify([
    adminSubtitleListScope(),
    provider,
    language,
    userId,
    search.trim(),
    pageSize,
  ]);
  const [pagination, setPagination] = useState({
    scope,
    cursors: [undefined] as (string | undefined)[],
  });
  // Derive the first page immediately on filter/authority changes; never send
  // an old cursor once under the new filters while waiting for an effect.
  const cursors = pagination.scope === scope ? pagination.cursors : [undefined];
  const page = cursors.length - 1;
  const filters = {
    provider: provider !== FILTER_ALL ? provider : undefined,
    language: language !== FILTER_ALL ? language : undefined,
    user_id: userId !== FILTER_ALL ? userId : undefined,
    q: search.trim() || undefined,
    limit: pageSize,
    cursor: cursors[page],
  };

  const subtitlesQuery = useAdminDownloadedSubtitles(filters);
  const subtitles = subtitlesQuery.data?.items ?? [];
  const total = subtitlesQuery.data?.total ?? 0;
  const uploads = subtitlesQuery.data?.uploads ?? 0;
  const providerDownloads = subtitlesQuery.data?.provider_downloads ?? 0;
  const languageCount = new Set(subtitles.map((row) => row.language)).size;

  const hasActiveFilters =
    provider !== FILTER_ALL ||
    language !== FILTER_ALL ||
    userId !== FILTER_ALL ||
    search.trim().length > 0;

  function updateFilter(key: string, value: string) {
    const next = new URLSearchParams(searchParams);
    if (value === FILTER_ALL || value.trim() === "") {
      next.delete(key);
    } else {
      next.set(key, value);
    }
    setPagination({ scope, cursors: [undefined] });
    setSearchParams(next, { replace: true });
  }

  function resetFilters() {
    setPagination({ scope, cursors: [undefined] });
    setSearchParams(new URLSearchParams(), { replace: true });
  }

  const canPrev = page > 0;
  const nextCursor = subtitlesQuery.data?.page?.next_cursor;
  const canNext =
    !!subtitlesQuery.data?.page?.has_more && !!nextCursor && !cursors.includes(nextCursor);

  if (subtitlesQuery.isLoading) {
    return (
      <div className="page-shell space-y-6 py-4 sm:py-6">
        <div className="space-y-3">
          <Skeleton className="h-12 w-72 rounded-lg" />
          <Skeleton className="h-5 w-full max-w-xl rounded-lg" />
        </div>
        <Skeleton className="h-20 w-full rounded-2xl" />
        <Skeleton className="h-24 w-full rounded-2xl" />
        {Array.from({ length: 6 }).map((_, index) => (
          <Skeleton key={index} className="h-12 w-full rounded-lg" />
        ))}
      </div>
    );
  }

  return (
    <div className="page-shell space-y-6 py-4 sm:py-6">
      <div className="page-header gap-5">
        <div className="space-y-3">
          <h1 className="page-title text-[clamp(2rem,4vw,3rem)]">Subtitles</h1>
          <p className="page-subtitle text-sm sm:text-base">
            Manage stored subtitle files across the library — user uploads and provider downloads.
          </p>
        </div>
      </div>

      <div className="surface-panel-subtle grid gap-4 rounded-2xl px-4 py-4 sm:grid-cols-2 xl:grid-cols-4">
        <StatBlock label="Total stored" value={total} />
        <StatBlock label="User uploads" value={uploads} />
        <StatBlock label="Provider downloads" value={providerDownloads} />
        <StatBlock label="Languages on page" value={languageCount} />
      </div>

      <AdminSubtitlesFilters
        provider={provider}
        language={language}
        userId={userId}
        search={search}
        users={users}
        onProviderChange={(value) => updateFilter("provider", value)}
        onLanguageChange={(value) => updateFilter("language", value)}
        onUserChange={(value) => updateFilter("user_id", value)}
        onSearchChange={(value) => updateFilter("q", value)}
        onReset={resetFilters}
      />

      {subtitlesQuery.isError && (
        <p role="alert">Unable to load subtitles. {subtitlesQuery.error.message}</p>
      )}

      <AdminSubtitlesTable
        authorityScope={adminSubtitleListScope()}
        subtitles={subtitles}
        hasActiveFilters={hasActiveFilters}
        onResetFilters={resetFilters}
      />

      {(total > 0 || page > 0) && (
        <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
          <p className="text-muted-foreground text-sm">
            {subtitles.length} subtitles on this page; {total} currently match
          </p>
          <div className="flex flex-wrap items-center gap-2">
            <Select
              value={String(pageSize)}
              onValueChange={(value) => {
                setPageSize(Number(value));
                setPagination({ scope, cursors: [undefined] });
              }}
            >
              <SelectTrigger className="w-[110px]">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {PAGE_SIZE_OPTIONS.map((size) => (
                  <SelectItem key={size} value={size}>
                    {size} / page
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Button
              type="button"
              variant="outline"
              disabled={!canPrev}
              onClick={() => setPagination({ scope, cursors: cursors.slice(0, -1) })}
            >
              Previous
            </Button>
            <span className="text-muted-foreground px-1 text-sm">Page {page + 1}</span>
            <Button
              type="button"
              variant="outline"
              disabled={!canNext}
              onClick={() => {
                if (nextCursor) setPagination({ scope, cursors: [...cursors, nextCursor] });
              }}
            >
              Next
            </Button>
          </div>
        </div>
      )}
    </div>
  );
}

function StatBlock({ label, value }: { label: string; value: number }) {
  return (
    <div className="space-y-1">
      <div className="text-muted-foreground text-xs font-medium tracking-[0.18em] uppercase">
        {label}
      </div>
      <div className="text-2xl font-semibold tracking-tight">{value.toLocaleString()}</div>
    </div>
  );
}
