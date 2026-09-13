export const SKIPPABLE_STEPS = [
  "server",
  "playback",
  "storage",
  "library",
  "subtitles",
  "connect",
  "features",
] as const;

export type SkippableStep = (typeof SKIPPABLE_STEPS)[number];

export const SETUP_WIZARD_STORAGE_KEYS: Record<SkippableStep, string> = {
  server: "setup_wizard_server_done",
  playback: "setup_wizard_playback_done",
  storage: "setup_wizard_storage_done",
  library: "setup_wizard_skip_library",
  subtitles: "setup_wizard_subtitles_done",
  connect: "setup_wizard_connect_done",
  features: "setup_wizard_features_done",
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
    // Sweep by prefix so flags from an older step layout go too.
    for (const key of Object.keys(storage)) {
      if (key.startsWith("setup_wizard_")) storage.removeItem(key);
    }
  }, undefined);
}
