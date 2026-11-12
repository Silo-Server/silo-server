import { useEffect, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";

import type { SettingsSectionEntry } from "@/api/types";
import { problemDisplayText, V2ProblemError } from "@/api/v2/request";
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
import { useT, type Translate } from "@/i18n";
import { TOUCH_TARGETS } from "@/lib/touchTargets";
import { fetchRecipeCatalog } from "@/lib/recipes";

import { DetailCard } from "../ui";

// A refused save names its reason in the problem's first error (a 400 from
// recipe validation) or in its detail.
function saveErrorMessage(t: Translate, error: unknown): string {
  let reason: string | undefined;
  if (error instanceof V2ProblemError) {
    const field = error.problem.errors?.[0];
    reason = problemDisplayText(error.problem, field ? field.detail : (error.problem.detail ?? ""));
  } else if (error instanceof Error) {
    reason = error.message;
  }
  reason = reason?.trim();
  return reason
    ? t("web.admin.users.detail.preferences.home.saveFailedReason", { reason })
    : t("web.admin.users.detail.preferences.home.saveFailed");
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
  const t = useT();
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
    // eslint-disable-next-line react-hooks/set-state-in-effect -- the editable list resets when the saved sections load or change
    if (settings.data) setSections(settings.data);
  }, [settings.data]);

  const ready = canMutateSectionSettings(settings, overrides);
  const busy = !ready || save.isPending || reset.isPending;

  function persist(next: SettingsSectionEntry[]) {
    if (!ready) return;
    setSections(next);
    const body = buildSectionOverrides(next, hydrateRemovedSystemSections(overrides.data), {
      savedOverrides: overrides.data,
      newId: newOverrideId.current,
      // Store only what changed, as the profile's own Home screen settings
      // do, so rows left alone keep following the server's layout.
      baseline: settings.data,
    });
    save.mutate(body, {
      onError: (error) => {
        toast.error(saveErrorMessage(t, error));
        if (settings.data) setSections(settings.data);
      },
    });
  }

  let body;
  if (settings.isLoading || overrides.isLoading) {
    body = (
      <p className="text-muted-foreground px-5 py-8 text-center text-sm">
        {t("web.admin.users.detail.preferences.home.loading")}
      </p>
    );
  } else if (settings.isError || overrides.isError) {
    body = (
      <p role="alert" className="text-destructive px-5 py-8 text-center text-sm">
        {t("web.admin.users.detail.preferences.home.loadFailed", {
          profile: profileName,
        })}
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
            persist(sections.map((s) => (s.id === section.id ? { ...s, hidden: !s.hidden } : s)))
          }
        />
      </div>
    );
  }

  return (
    <>
      <DetailCard
        title={t("web.admin.users.detail.preferences.home.title", { profile: profileName })}
        description={t("web.admin.users.detail.preferences.home.description", {
          profile: profileName,
        })}
        actions={
          <Button variant="outline" size="sm" disabled={busy} onClick={() => setConfirmReset(true)}>
            {t("web.admin.users.detail.preferences.home.reset")}
          </Button>
        }
      >
        {body}
      </DetailCard>
      <ConfirmDialog
        open={confirmReset}
        onOpenChange={setConfirmReset}
        title={t("web.admin.users.detail.preferences.home.resetTitle", {
          profile: profileName,
        })}
        description={t("web.admin.users.detail.preferences.home.resetDescription", {
          profile: profileName,
        })}
        confirmLabel={t("web.admin.users.detail.preferences.home.resetConfirm")}
        className={TOUCH_TARGETS}
        onConfirm={() => {
          setConfirmReset(false);
          reset.mutate(undefined, {
            onSuccess: () =>
              toast.success(
                t("web.admin.users.detail.preferences.home.resetDone", {
                  profile: profileName,
                }),
              ),
            onError: () => toast.error(t("web.admin.users.detail.preferences.home.resetFailed")),
          });
        }}
      />
    </>
  );
}
