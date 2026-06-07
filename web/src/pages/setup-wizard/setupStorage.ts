export const SKIPPABLE_STEPS = [
  "library",
  "server",
  "integrations",
  "downloads",
  "recommendations",
] as const;

export type SkippableStep = (typeof SKIPPABLE_STEPS)[number];

export const SETUP_WIZARD_STORAGE_KEYS: Record<SkippableStep, string> = {
  library: "setup_wizard_skip_library",
  server: "setup_wizard_server_done",
  integrations: "setup_wizard_integrations_done",
  downloads: "setup_wizard_downloads_done",
  recommendations: "setup_wizard_recommendations_done",
};

function withLocalStorage<T>(callback: (storage: Storage) => T, fallback: T): T {
  if (typeof window === "undefined") return fallback;

  try {
    return callback(window.localStorage);
  } catch {
    return fallback;
  }
}

export function createEmptySetupWizardFlags(): Record<SkippableStep, boolean> {
  const flags = {} as Record<SkippableStep, boolean>;
  for (const step of SKIPPABLE_STEPS) {
    flags[step] = false;
  }
  return flags;
}

export function readSetupWizardFlag(step: SkippableStep): boolean {
  return withLocalStorage(
    (storage) => storage.getItem(SETUP_WIZARD_STORAGE_KEYS[step]) === "true",
    false,
  );
}

export function readSetupWizardFlags(): Record<SkippableStep, boolean> {
  const flags = createEmptySetupWizardFlags();
  for (const step of SKIPPABLE_STEPS) {
    flags[step] = readSetupWizardFlag(step);
  }
  return flags;
}

export function writeSetupWizardFlag(step: SkippableStep, value: boolean) {
  withLocalStorage((storage) => {
    if (value) {
      storage.setItem(SETUP_WIZARD_STORAGE_KEYS[step], "true");
    } else {
      storage.removeItem(SETUP_WIZARD_STORAGE_KEYS[step]);
    }
  }, undefined);
}

export function clearSetupWizardStorage() {
  withLocalStorage((storage) => {
    for (const key of Object.values(SETUP_WIZARD_STORAGE_KEYS)) {
      storage.removeItem(key);
    }
  }, undefined);
}
