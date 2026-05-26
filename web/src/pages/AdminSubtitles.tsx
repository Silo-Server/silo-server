import { useMemo, useState } from "react";
import { useSearchParams } from "react-router";
import type { AdminDownloadedSubtitle } from "@/api/types";
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
import {
  useAdminDeleteDownloadedSubtitle,
  useAdminDownloadedSubtitles,
} from "@/hooks/queries/admin/subtitles";
import { useAdminUsers } from "@/hooks/queries/admin/users";

const PAGE_SIZE_OPTIONS = ["25", "50", "100"] as const;

export default function AdminSubtitles() {
  const [searchParams, setSearchParams] = useSearchParams();
  const { data: users = [] } = useAdminUsers();
  const [page, setPage] = useState(0);
  const [pageSize, setPageSize] = useState(25);
  const deleteMutation = useAdminDeleteDownloadedSubtitle();

  const provider = searchParams.get("provider") ?? FILTER_ALL;
  const language = searchParams.get("language") ?? FILTER_ALL;
  const userId = searchParams.get("user_id") ?? FILTER_ALL;
  const search = searchParams.get("q") ?? "";

  const filters = useMemo(
    () => ({
      provider: provider !== FILTER_ALL ? provider : undefined,
      language: language !== FILTER_ALL ? language : undefined,
      userId: userId !== FILTER_ALL ? Number(userId) : undefined,
      q: search.trim() || undefined,
      limit: pageSize,
      offset: page * pageSize,
    }),
    [language, page, pageSize, provider, search, userId],
  );

  const subtitlesQuery = useAdminDownloadedSubtitles(filters);
  const subtitles = subtitlesQuery.data?.subtitles ?? [];
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
    setPage(0);
    setSearchParams(next, { replace: true });
  }

  function resetFilters() {
    setPage(0);
    setSearchParams(new URLSearchParams(), { replace: true });
  }

  function handleDelete(subtitle: AdminDownloadedSubtitle) {
    deleteMutation.mutate(subtitle.id);
  }

  const pageCount = Math.max(1, Math.ceil(total / pageSize));
  const canPrev = page > 0;
  const canNext = (page + 1) * pageSize < total;

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

      <AdminSubtitlesTable
        subtitles={subtitles}
        hasActiveFilters={hasActiveFilters}
        onResetFilters={resetFilters}
        onDelete={handleDelete}
        isDeleting={deleteMutation.isPending}
      />

      {total > 0 && (
        <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
          <p className="text-muted-foreground text-sm">
            Showing {page * pageSize + 1}–{Math.min((page + 1) * pageSize, total)} of {total}
          </p>
          <div className="flex flex-wrap items-center gap-2">
            <Select
              value={String(pageSize)}
              onValueChange={(value) => {
                setPageSize(Number(value));
                setPage(0);
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
              onClick={() => setPage((p) => p - 1)}
            >
              Previous
            </Button>
            <span className="text-muted-foreground px-1 text-sm">
              Page {page + 1} of {pageCount}
            </span>
            <Button
              type="button"
              variant="outline"
              disabled={!canNext}
              onClick={() => setPage((p) => p + 1)}
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
