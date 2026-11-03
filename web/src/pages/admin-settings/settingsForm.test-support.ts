import { vi } from "vitest";

type FakeFormOptions = {
  /** Keys staged in the save bar. */
  dirty?: string[];
  /** What the server holds; defaults to `values`. */
  persisted?: Record<string, string>;
} & Record<string, unknown>;

/**
 * A loaded, idle `useSettingsForm` result for page tests that build the form
 * by hand. `values` are the current values; any other field of the result can
 * be replaced through the options.
 */
export function fakeSettingsForm(
  values: Record<string, string> = {},
  { dirty = [], persisted = values, ...overrides }: FakeFormOptions = {},
) {
  return {
    isLoading: false,
    isPending: false,
    loadError: false,
    loaded: true,
    retryLoad: vi.fn(),
    getValue: (key: string) => values[key] ?? "",
    getPersistedValue: (key: string) => persisted[key] ?? values[key] ?? "",
    setValue: vi.fn(),
    resetValue: vi.fn(),
    isDirty: (key: string) => dirty.includes(key),
    isClearStaged: (key: string) => dirty.includes(key) && (values[key] ?? "") === "",
    dirtyCount: dirty.length,
    dirtyKeys: dirty,
    save: vi.fn(),
    discard: vi.fn(),
    isSaving: false,
    restartRequired: false,
    sensitiveConfigured: [] as string[],
    sensitiveManagedByEnv: [] as string[],
    sensitiveStatusReady: true,
    sensitiveStatusError: false,
    buildConnectionCheckRequest: vi.fn(),
    ...overrides,
  };
}
