import { render } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";

import { appearanceCache, storage } from "@/utils/storage";

vi.mock("@/hooks/useAuth", () => ({ useOptionalAuth: () => null }));
vi.mock("@/hooks/queries/settingValues", () => ({
  isDefinitiveSettingMutationRejection: () => true,
  useEffectiveSettings: () => ({ data: undefined, isError: false, fetchStatus: "idle" }),
  useSetSettingValue: () => ({ mutate: vi.fn(), mutateAsync: vi.fn(), isPending: false }),
  useClearSettingValue: () => ({ mutate: vi.fn(), mutateAsync: vi.fn(), isPending: false }),
}));

import { ThemeProvider } from "@/hooks/useTheme";
import { useLibrariesTableLayout, useWideTableLayout } from "./diagnosticsListState";

let asked: string[] = [];

beforeEach(() => {
  asked = [];
  vi.stubGlobal(
    "matchMedia",
    vi.fn((query: string) => {
      asked.push(query);
      return {
        matches: false,
        media: query,
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
      };
    }),
  );
});
afterEach(() => {
  vi.unstubAllGlobals();
  appearanceCache.remove(storage.KEYS.UI_TEXT_SCALE, null);
});

function Probe() {
  useLibrariesTableLayout();
  useWideTableLayout();
  return null;
}

// The tables grow with the app's text size, and a media query's rem doesn't,
// so their switch to cards moves out by the same factor (#5071).
it.each([
  ["default", ["(min-width: 64rem)", "(min-width: 80rem)"]],
  ["large", ["(min-width: 72rem)", "(min-width: 90rem)"]],
  ["x-large", ["(min-width: 80rem)", "(min-width: 100rem)"]],
])("switches the admin tables to cards later at %s text", (scale, queries) => {
  appearanceCache.set(storage.KEYS.UI_TEXT_SCALE, scale, null);
  render(
    <ThemeProvider>
      <Probe />
    </ThemeProvider>,
  );
  for (const query of queries) expect(asked).toContain(query);
});
