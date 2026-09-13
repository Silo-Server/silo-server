import { createContext, useCallback, useContext, useEffect, useState } from "react";
import type { ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { listProfiles } from "@/hooks/queries/profiles";
import type { Library, Profile } from "@/api/types";
import { fetchAdminLibraries } from "@/hooks/queries/admin/libraries";
import { useAdminServerSettings } from "@/hooks/queries/admin/settings";
import { useSubtitleProviders } from "@/hooks/queries/admin/subtitles";
import { getBootstrapProfile, useAuth } from "@/hooks/useAuth";
import {
  clearSetupWizardStorage,
  createEmptySetupWizardFlags,
  readSetupWizardFlags,
  type SkippableStep,
  writeSetupWizardFlag,
} from "./setupStorage";

interface WizardContextValue {
  // Auth-derived
  user: ReturnType<typeof useAuth>["user"];
  profile: ReturnType<typeof useAuth>["profile"];
  setupInitialUser: ReturnType<typeof useAuth>["setupInitialUser"];
  refreshSetupStatus: ReturnType<typeof useAuth>["refreshSetupStatus"];
  selectProfile: ReturnType<typeof useAuth>["selectProfile"];

  // Queries
  profiles: Profile[];
  /** The household profile list has been read at least once. */
  profilesLoaded: boolean;
  /**
   * Every household profile has a PIN, so the wizard cannot pick one itself;
   * the admin has to unlock one on the profile picker first.
   */
  profilesNeedPin: boolean;
  /** The household profile list could not be read; the wizard cannot continue without it. */
  profilesError: boolean;
  retryProfiles: () => void;
  /**
   * The shared settings snapshot has arrived (or failed, or cannot load
   * because no profile is selected). The account step stays on screen until
   * this is true so the first settings step lands drawn, not as a skeleton.
   */
  settingsReady: boolean;
  libraries: Library[];
  librariesLoading: boolean;
  refetchLibraries: () => void;

  // Step completion flags
  stepDone: Record<SkippableStep, boolean>;
  markDone: (step: SkippableStep) => void;
  clearProgress: () => void;

  /**
   * Short summaries of what each finished step chose, shown on the rail
   * ("NVIDIA NVENC", "2 libraries"). Kept in memory only: they are a courtesy
   * for the current visit, not state the wizard depends on.
   */
  summaries: Partial<Record<SkippableStep | "account", string>>;
  setSummary: (step: SkippableStep | "account", summary: string | undefined) => void;

  /** A completed step the admin reopened from the rail, if any. */
  visiting: SkippableStep | null;
  visit: (step: SkippableStep | null) => void;
}

const WizardCtx = createContext<WizardContextValue | null>(null);

export function useWizardContext() {
  const ctx = useContext(WizardCtx);
  if (!ctx) throw new Error("useWizardContext must be used within WizardProvider");
  return ctx;
}

function shouldRetrySetupQuery(failureCount: number, error: unknown) {
  if (error instanceof Error && "status" in error && (error as { status: number }).status === 429) {
    return false;
  }
  return failureCount < 1;
}

export function WizardProvider({ children }: { children: ReactNode }) {
  const { user, profile, setupRequired, setupInitialUser, selectProfile, refreshSetupStatus } =
    useAuth();
  const isAdmin = user?.role === "admin";

  const [stepDone, setStepDone] = useState<Record<SkippableStep, boolean>>(() => {
    if (setupRequired && !user) {
      clearSetupWizardStorage();
      return createEmptySetupWizardFlags();
    }
    return readSetupWizardFlags();
  });
  const [summaries, setSummaries] = useState<WizardContextValue["summaries"]>({});
  const [visiting, setVisiting] = useState<SkippableStep | null>(null);

  // Every settings step reads the same snapshot, so one warm copy here means
  // no step after the first ever shows a skeleton. The provider list is the
  // one other read a step needs on entry; it is cheap and 30s fresh.
  const settingsQuery = useAdminServerSettings();
  useSubtitleProviders(isAdmin && profile !== null);
  const settingsReady = settingsQuery.data !== undefined || settingsQuery.isError;

  const profilesQuery = useQuery({
    queryKey: ["setup-wizard", "profiles"],
    queryFn: () => listProfiles().then((d) => d.profiles),
    enabled: !!user,
    retry: shouldRetrySetupQuery,
  });

  // Account creation selects the default profile itself, but that lookup can
  // fail on a flaky connection. This query is the wizard's own retryable
  // path to the same profile, so a stalled account step can always recover.
  const profileList = profilesQuery.data;
  useEffect(() => {
    if (!user || profile || !profileList) return;
    // The sole profile when there is one, otherwise the first without a PIN:
    // the wizard runs as the admin and cannot answer a PIN prompt here.
    const chosen = getBootstrapProfile(profileList) ?? profileList.find((p) => !p.has_pin);
    if (chosen) selectProfile(chosen);
  }, [user, profile, profileList, selectProfile]);

  // Acting-admin reads need a selected profile, same as the settings and
  // provider queries above; starting earlier would burn the retries before
  // the profile arrives and leave the Library step showing an empty list.
  const librariesQuery = useQuery({
    queryKey: ["setup-wizard", "libraries"],
    queryFn: ({ signal }) => fetchAdminLibraries(signal),
    enabled: isAdmin && profile !== null,
    retry: shouldRetrySetupQuery,
  });

  const markDone = useCallback((step: SkippableStep) => {
    writeSetupWizardFlag(step, true);
    setStepDone((prev) => ({ ...prev, [step]: true }));
    setVisiting(null);
  }, []);

  const clearProgress = useCallback(() => {
    clearSetupWizardStorage();
    setStepDone(createEmptySetupWizardFlags());
    setVisiting(null);
  }, []);

  const setSummary = useCallback((step: SkippableStep | "account", summary: string | undefined) => {
    setSummaries((prev) => {
      if (prev[step] === summary) return prev;
      return { ...prev, [step]: summary };
    });
  }, []);

  return (
    <WizardCtx.Provider
      value={{
        user,
        profile,
        setupInitialUser,
        refreshSetupStatus,
        selectProfile,
        profiles: profilesQuery.data ?? [],
        profilesLoaded: profilesQuery.data !== undefined,
        profilesNeedPin:
          profileList !== undefined &&
          profileList.length > 0 &&
          profileList.every((p) => p.has_pin),
        profilesError: profilesQuery.isError,
        retryProfiles: () => void profilesQuery.refetch(),
        settingsReady,
        libraries: librariesQuery.data ?? [],
        librariesLoading: isAdmin && profile !== null && librariesQuery.isPending,
        refetchLibraries: () => void librariesQuery.refetch(),
        stepDone,
        markDone,
        clearProgress,
        summaries,
        setSummary,
        visiting,
        visit: setVisiting,
      }}
    >
      {children}
    </WizardCtx.Provider>
  );
}
