import { useEffect, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";

import type { SettingsSectionEntry } from "@/api/types";
import { V2ProblemError } from "@/api/v2/request";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { SectionOrderList } from "@/components/sections/SectionOrderList";
import { Button } from "@/components/ui/button";
import {
  useAdminProfileSectionOverrides,
  useAdminProfileSectionSettings,
  useResetAdminProfileSections,
  useSaveAdminProfileSections,
} from "@/hooks/queries/admin/profileSections";
import {
  buildSectionOverrides,
  canMutateSectionSettings,
  createOverrideIdSource,
  hydrateRemovedSystemSections,
} from "@/lib/homeRows/profileOverrides";
import { fetchRecipeCatalog } from "@/lib/recipes";

import { DetailCard } from "../ui";

// A refused save names its reason in the problem's first error (a 400 from
// the section source policy or recipe validation) or in its detail.
function saveErrorMessage(error: unknown): string {
  let reason: string | undefined;
  if (error instanceof V2ProblemError) {
    reason = error.problem.errors?.[0]?.detail ?? error.problem.detail;
  } else if (error instanceof Error) {
    reason = error.message;
  }
  reason = reason?.trim();
  return reason ? `Couldn't save Home sections: ${reason}` : "Couldn't save Home sections";
}

/**
 * One profile's Home section order: drag to reorder, the eye to show or hide,
 * and reset to the server layout. It saves the same override set the
 * profile's own Home screen settings save.
 */
export function ProfileHomeSections({
  userId,
  profileId,
  profileName,
}: {
  userId: number;
  profileId: string;
  profileName: string;
}) {
  const settings = useAdminProfileSectionSettings(userId, profileId);
  const overrides = useAdminProfileSectionOverrides(userId, profileId);
  const save = useSaveAdminProfileSections(userId, profileId);
  const reset = useResetAdminProfileSections(userId, profileId);
  const { data: catalog } = useQuery({
    queryKey: ["recipe-catalog"],
    queryFn: fetchRecipeCatalog,
    staleTime: 5 * 60 * 1000,
  });

  const [sections, setSections] = useState<SettingsSectionEntry[]>([]);
  const [confirmReset, setConfirmReset] = useState(false);
  const newOverrideId = useRef(createOverrideIdSource());

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    if (settings.data) setSections(settings.data);
  }, [settings.data]);

  const ready = canMutateSectionSettings(settings, overrides);
  const busy = !ready || save.isPending || reset.isPending;

  function persist(next: SettingsSectionEntry[], changedSectionId: string) {
    if (!ready) return;
    setSections(next);
    const body = buildSectionOverrides(next, hydrateRemovedSystemSections(overrides.data), {
      savedOverrides: overrides.data,
      newId: newOverrideId.current,
      changedSectionId,
      // Store only what changed, as the profile's own Home screen settings
      // do, so rows left alone keep following the server's layout.
      baseline: settings.data,
    });
    save.mutate(body, {
      onError: (error) => {
        toast.error(saveErrorMessage(error));
        if (settings.data) setSections(settings.data);
      },
    });
  }

  let body;
  if (settings.isLoading || overrides.isLoading) {
    body = <p className="text-muted-foreground px-5 py-8 text-center text-sm">Loading sections…</p>;
  } else if (settings.isError || overrides.isError) {
    body = (
      <p role="alert" className="text-destructive px-5 py-8 text-center text-sm">
        Couldn&apos;t load {profileName}&apos;s Home sections.
      </p>
    );
  } else {
    body = (
      <div className="px-4 py-3 sm:px-5">
        <SectionOrderList
          sections={sections}
          catalog={catalog}
          disabled={busy}
          onMove={persist}
          onToggleHidden={(section) =>
            persist(
              sections.map((s) => (s.id === section.id ? { ...s, hidden: !s.hidden } : s)),
              section.id,
            )
          }
        />
      </div>
    );
  }

  return (
    <>
      <DetailCard
        title={`Home sections · ${profileName}`}
        description={`The rows on ${profileName}'s Home screen. Drag to reorder; the eye shows or hides a row.`}
        actions={
          <Button variant="outline" size="sm" disabled={busy} onClick={() => setConfirmReset(true)}>
            Reset to default
          </Button>
        }
      >
        {body}
      </DetailCard>
      <ConfirmDialog
        open={confirmReset}
        onOpenChange={setConfirmReset}
        title={`Reset ${profileName}'s Home sections?`}
        description={`${profileName}'s Home goes back to the server's section order and visibility, and rows ${profileName} added are removed. Other profiles keep theirs.`}
        confirmLabel="Reset"
        onConfirm={() => {
          setConfirmReset(false);
          reset.mutate(undefined, {
            onSuccess: () => toast.success(`${profileName}'s Home sections reset`),
            onError: () => toast.error("Couldn't reset Home sections"),
          });
        }}
      />
    </>
  );
}
