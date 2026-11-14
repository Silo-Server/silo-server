import "@testing-library/jest-dom";
import { vi } from "vitest";

import { loadAdminEnglishForTests } from "@/i18n";

// Tests render admin pages and call admin helpers without the admin layout
// that would load their text.
await loadAdminEnglishForTests();

// Components render inside the app's message provider. Tests render them alone,
// so every render and renderHook gets the English one, which formats from the
// generated English catalog and throws on a formatting error. A test's own
// wrapper still applies, inside this one, and rerender keeps both.
vi.mock("@testing-library/react", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@testing-library/react")>();
  const { createElement } = await import("react");
  const { AppIntlProvider, createAppIntl } = await import("@/i18n");
  const english = createAppIntl("en", {});
  type Wrapper = React.JSXElementConstructor<{ children: React.ReactNode }>;

  function withEnglish(wrapper: Wrapper | undefined): Wrapper {
    return function EnglishIntl({ children }) {
      return createElement(
        AppIntlProvider,
        { intl: english },
        wrapper ? createElement(wrapper, null, children) : children,
      );
    };
  }

  const render = ((ui: React.ReactNode, options?: { wrapper?: Wrapper }) =>
    actual.render(ui, {
      ...options,
      wrapper: withEnglish(options?.wrapper),
    })) as typeof actual.render;
  const renderHook = ((callback: (props: unknown) => unknown, options?: { wrapper?: Wrapper }) =>
    actual.renderHook(callback, {
      ...options,
      wrapper: withEnglish(options?.wrapper),
    })) as typeof actual.renderHook;

  return { ...actual, render, renderHook };
});
