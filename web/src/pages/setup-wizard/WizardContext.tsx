import { createContext, useCallback, useContext, useState } from "react";
import type { ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { api } from "@/api/client";
import type { Library, Profile } from "@/api/types";
import { useAuth } from "@/hooks/useAuth";
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
  setupRequired: boolean;
  setupInitialUser: ReturnType<typeof useAuth>["setupInitialUser"];
  selectProfile: ReturnType<typeof useAuth>["selectProfile"];

  // Queries
  profiles: Profile[];
  profilesLoading: boolean;
  refetchProfiles: () => void;
  libraries: Library[];
  librariesLoading: boolean;
  refetchLibraries: () => void;

  // Step completion flags
  stepDone: Record<SkippableStep, boolean>;
  markDone: (step: SkippableStep) => void;
  clearProgress: () => void;
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
  const { user, profile, setupRequired, setupInitialUser, selectProfile } = useAuth();
  const isAdmin = user?.role === "admin";

  const [stepDone, setStepDone] = useState<Record<SkippableStep, boolean>>(() => {
    if (setupRequired && !user) {
      clearSetupWizardStorage();
      return createEmptySetupWizardFlags();
    }
    return readSetupWizardFlags();
  });

  const profilesQuery = useQuery({
    queryKey: ["setup-wizard", "profiles"],
    queryFn: () => api<{ profiles: Profile[] }>("/profiles").then((d) => d.profiles ?? []),
    enabled: !!user,
    retry: shouldRetrySetupQuery,
  });

  const profileComplete = (profilesQuery.data ?? []).length > 0;

  const librariesQuery = useQuery({
    queryKey: ["setup-wizard", "libraries"],
    queryFn: () => api<Library[]>("/libraries").then((d) => d ?? []),
    enabled: isAdmin && profileComplete,
    retry: shouldRetrySetupQuery,
  });

  const markDone = useCallback((step: SkippableStep) => {
    writeSetupWizardFlag(step, true);
    setStepDone((prev) => ({ ...prev, [step]: true }));
  }, []);

  const clearProgress = useCallback(() => {
    clearSetupWizardStorage();
    setStepDone(createEmptySetupWizardFlags());
  }, []);

  return (
    <WizardCtx.Provider
      value={{
        user,
        profile,
        setupRequired,
        setupInitialUser,
        selectProfile,
        profiles: profilesQuery.data ?? [],
        profilesLoading: !!user && profilesQuery.isPending,
        refetchProfiles: () => void profilesQuery.refetch(),
        libraries: librariesQuery.data ?? [],
        librariesLoading: isAdmin && profileComplete && librariesQuery.isPending,
        refetchLibraries: () => void librariesQuery.refetch(),
        stepDone,
        markDone,
        clearProgress,
      }}
    >
      {children}
    </WizardCtx.Provider>
  );
}
