import "@testing-library/jest-dom";
import { vi } from "vitest";

// Components render inside the app's IntlProvider. Tests render them alone, so
// every render and renderHook gets an English provider with no catalog: each
// message shows its English default, as before extraction. A test's own
// wrapper still applies, inside this one, and rerender keeps both.
vi.mock("@testing-library/react", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@testing-library/react")>();
  const { createElement } = await import("react");
  const { IntlProvider } = await import("react-intl");
  type Wrapper = React.JSXElementConstructor<{ children: React.ReactNode }>;

  function withEnglish(wrapper: Wrapper | undefined): Wrapper {
    return function EnglishIntl({ children }) {
      return createElement(
        IntlProvider,
        { locale: "en", defaultLocale: "en", messages: {} },
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
