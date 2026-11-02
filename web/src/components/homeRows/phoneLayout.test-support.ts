import { vi } from "vitest";

/** A phone: under 1024px (docked bar, picker sheet) and under 640px. */
export function stubPhone() {
  vi.stubGlobal(
    "matchMedia",
    (query: string) =>
      ({
        matches: query === "(max-width: 1023px)" || query === "(max-width: 639px)",
        media: query,
        addEventListener() {},
        removeEventListener() {},
      }) as unknown as MediaQueryList,
  );
}
