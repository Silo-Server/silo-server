import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { LocaleProvider, setSignedOutLanguage } from "@/hooks/useAppLocale";
import { appIntl, loadTranslations } from "@/i18n";
import { storage } from "@/utils/storage";

vi.mock("@/i18n", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/i18n")>()),
  loadTranslations: vi.fn(),
}));

afterEach(() => {
  storage.remove(storage.KEYS.UI_APP_LANGUAGE_SIGNED_OUT);
});

describe("LocaleProvider", () => {
  it.each([
    {
      name: "applies the language once its catalog loads",
      load: "resolve",
      lang: "ar",
      dir: "rtl",
      // The formatter names the digits of the format locale, the chosen `ar`.
      intlLocale: `ar-u-nu-${new Intl.NumberFormat("ar").resolvedOptions().numberingSystem}`,
    },
    {
      name: "falls back to English when its catalog fails",
      load: "reject",
      lang: "en",
      dir: "ltr",
      intlLocale: "en",
    },
  ] as const)("$name", async ({ load, lang, dir, intlLocale }) => {
    vi.mocked(loadTranslations).mockImplementation(() =>
      load === "resolve" ? Promise.resolve({}) : Promise.reject(new Error("chunk gone")),
    );
    setSignedOutLanguage("ar");
    document.documentElement.lang = "";

    render(
      <QueryClientProvider client={new QueryClient()}>
        <LocaleProvider>page</LocaleProvider>
      </QueryClientProvider>,
    );

    await waitFor(() => expect(document.documentElement.lang).not.toBe(""));
    expect(document.documentElement).toHaveAttribute("lang", lang);
    expect(document.documentElement).toHaveAttribute("dir", dir);
    expect(appIntl().locale).toBe(intlLocale);
  });
});
