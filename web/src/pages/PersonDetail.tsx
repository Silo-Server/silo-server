import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useParams } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { Pencil, RefreshCw } from "lucide-react";

import { getPerson } from "@/api/client";
import { createEmptyQueryDefinition, type Person } from "@/api/types";
import type { CatalogSearchState } from "@/pages/catalogSearchParams";
import EditPersonDialog from "@/components/EditPersonDialog";
import ItemGrid from "@/components/ItemGrid";
import PageBack from "@/components/PageBack";
import { Button } from "@/components/ui/button";
import { useCatalogWindow } from "@/hooks/queries/catalog";
import { personKeys } from "@/hooks/queries/keys";
import { useRefreshPerson } from "@/hooks/queries/people";
import { useAuth } from "@/hooks/useAuth";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";
import { Skeleton } from "@/components/ui/skeleton";
import { formatBirthDate, computeAge } from "@/lib/date";
import { getInitials } from "@/lib/text";

type TypeFilter = "all" | "movie" | "series";

export default function PersonDetail() {
  const { id } = useParams<{ id: string }>();
  const [typeFilter, setTypeFilter] = useState<TypeFilter>("all");
  const [editOpen, setEditOpen] = useState(false);
  const autoRefreshWindowRef = useRef<{ personId: number; until: number } | null>(null);
  const autoRefreshRequestedPersonIdRef = useRef<number | null>(null);
  const { user } = useAuth();
  const isAdmin = user?.role === "admin";
  const refreshMutation = useRefreshPerson(id, isAdmin);

  const { data: person, isLoading: personLoading } = useQuery({
    queryKey: personKeys.detail(id!),
    queryFn: () => getPerson(id!),
    enabled: !!id,
    refetchInterval: (query) => {
      const data = query.state.data;
      if (!data || !isPersonMetadataIncomplete(data)) {
        autoRefreshWindowRef.current = null;
        return false;
      }

      const current = autoRefreshWindowRef.current;
      if (!current || current.personId !== data.id) {
        autoRefreshWindowRef.current = { personId: data.id, until: Date.now() + 30_000 };
        return 3_000;
      }

      return Date.now() < current.until ? 3_000 : false;
    },
  });

  useDocumentTitle(person?.name ?? "Person");

  useEffect(() => {
    if (!person || !user || !isPersonMetadataIncomplete(person)) {
      return;
    }
    if (autoRefreshRequestedPersonIdRef.current === person.id || refreshMutation.isPending) {
      return;
    }
    autoRefreshRequestedPersonIdRef.current = person.id;
    refreshMutation.mutate();
  }, [person, refreshMutation, user]);

  const catalogState: CatalogSearchState = useMemo(
    () => ({
      source: "person",
      person_id: id,
      query_definition: {
        ...createEmptyQueryDefinition(),
        media_scope: typeFilter === "all" ? undefined : typeFilter,
        sort: { field: "year" as const, order: "desc" as const },
      },
    }),
    [id, typeFilter],
  );

  const limit = 60;
  const [visibleRange, setVisibleRange] = useState<[number, number]>([0, limit - 1]);
  const debounceRef = useRef<ReturnType<typeof setTimeout>>(undefined);
  const handleVisibleRangeChange = useCallback((start: number, end: number) => {
    clearTimeout(debounceRef.current);
    debounceRef.current = setTimeout(() => {
      setVisibleRange([start, end]);
    }, 50);
  }, []);
  useEffect(() => () => clearTimeout(debounceRef.current), []);
  const catalogQuery = useCatalogWindow(catalogState, { limit, visibleRange });

  if (personLoading) {
    return <PersonDetailSkeleton />;
  }

  if (!person) {
    return (
      <div className="page-shell flex min-h-[40vh] items-center justify-center">
        <p className="text-muted-foreground">Person not found.</p>
      </div>
    );
  }

  return (
    <div>
      {/* Person Header */}
      <section className="page-shell relative pt-8 pb-6 sm:pt-10 sm:pb-8">
        <PageBack />
        <div className="mt-10 flex flex-col gap-6 sm:mt-12 lg:flex-row lg:gap-8">
          {/* Photo */}
          <div className="shrink-0 self-start">
            <div className="media-card-image aspect-[2/3] w-[140px] overflow-hidden rounded-lg sm:w-[180px]">
              {person.photo_url ? (
                <img
                  src={person.photo_url}
                  alt={person.name}
                  className="h-full w-full object-cover"
                />
              ) : (
                <div className="bg-surface text-muted-foreground flex h-full w-full items-center justify-center text-3xl font-semibold">
                  {getInitials(person.name)}
                </div>
              )}
            </div>
          </div>

          {/* Info */}
          <div className="min-w-0 flex-1 pt-1">
            <div className="mb-3 flex flex-wrap items-center gap-3">
              <h1 className="text-2xl font-semibold tracking-tight sm:text-3xl">{person.name}</h1>
              {id && user && (
                <div className="flex flex-wrap gap-2">
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    onClick={() => refreshMutation.mutate()}
                    disabled={refreshMutation.isPending}
                  >
                    <RefreshCw
                      className={`h-3.5 w-3.5 ${refreshMutation.isPending ? "animate-spin" : ""}`}
                    />
                    {refreshMutation.isPending
                      ? isAdmin
                        ? "Refreshing..."
                        : "Queueing..."
                      : isAdmin
                        ? "Refresh now"
                        : "Refresh metadata"}
                  </Button>
                  {isAdmin ? (
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      onClick={() => setEditOpen(true)}
                    >
                      <Pencil className="h-3.5 w-3.5" />
                      Edit metadata
                    </Button>
                  ) : null}
                </div>
              )}
            </div>

            {/* Metadata badges */}
            <div className="mb-4 flex flex-wrap items-center gap-2">
              {person.birth_date && (
                <span className="metadata-badge">Born {formatBirthDate(person.birth_date)}</span>
              )}
              {person.birth_date && !person.death_date && (
                <span className="metadata-badge">{computeAge(person.birth_date)} years old</span>
              )}
              {person.death_date && person.birth_date && (
                <span className="metadata-badge">
                  Died {formatBirthDate(person.death_date)} (age{" "}
                  {computeAge(person.birth_date, person.death_date)})
                </span>
              )}
              {person.birthplace && <span className="metadata-badge">{person.birthplace}</span>}
            </div>

            {/* Bio */}
            {person.bio && (
              <p className="text-muted-foreground max-w-2xl text-sm leading-relaxed">
                {person.bio}
              </p>
            )}
          </div>
        </div>
      </section>

      {/* Divider */}
      <div className="border-border/10 border-t" />

      {/* Filmography */}
      <div className="page-shell space-y-6 py-6 sm:py-8">
        <div className="flex items-center justify-between">
          <h2 className="text-xl font-semibold tracking-tight">Filmography</h2>
          <div className="flex gap-1.5">
            {(["all", "movie", "series"] as const).map((t) => (
              <button
                key={t}
                type="button"
                onClick={() => {
                  setTypeFilter(t);
                  setVisibleRange([0, limit - 1]);
                }}
                className={`rounded-md border px-3 py-1.5 text-xs font-medium transition-colors ${
                  typeFilter === t
                    ? "border-border/20 bg-foreground/10 text-foreground"
                    : "border-border/10 text-muted-foreground hover:text-foreground"
                }`}
              >
                {t === "all" ? "All" : t === "movie" ? "Movies" : "Series"}
              </button>
            ))}
          </div>
        </div>

        <ItemGrid
          totalItems={catalogQuery.data?.totalItems ?? 0}
          pages={catalogQuery.data?.pages ?? new Map()}
          pageSize={limit}
          loading={catalogQuery.isLoading}
          onVisibleRangeChange={handleVisibleRangeChange}
        />
      </div>

      {isAdmin ? (
        <EditPersonDialog
          key={`${person.id}-${editOpen ? "open" : "closed"}`}
          person={person}
          open={editOpen}
          onOpenChange={setEditOpen}
        />
      ) : null}
    </div>
  );
}

function isPersonMetadataIncomplete(person: Person) {
  return !person.bio || !person.photo_url || !person.birth_date;
}

function PersonDetailSkeleton() {
  return (
    <div className="page-shell pt-8 sm:pt-10">
      <div className="flex flex-col gap-6 lg:flex-row lg:gap-8">
        <Skeleton className="aspect-[2/3] w-[140px] shrink-0 rounded-lg sm:w-[180px]" />
        <div className="flex-1 space-y-3 pt-1">
          <Skeleton className="h-8 w-48" />
          <div className="flex gap-2">
            <Skeleton className="h-5 w-32" />
            <Skeleton className="h-5 w-24" />
          </div>
          <Skeleton className="h-16 w-full max-w-2xl" />
        </div>
      </div>
    </div>
  );
}
