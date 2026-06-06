import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import AdminPlugins from "./AdminPlugins";

const useAdminPluginsMock = vi.fn();
const checkPluginUpdatesMutateMock = vi.fn();
const capturedButtonProps: Array<Record<string, unknown>> = [];

vi.mock("@/components/ui/button", () => ({
  Button: (props: Record<string, unknown>) => {
    capturedButtonProps.push(props);
    return props.children;
  },
}));

vi.mock("@/components/ui/tabs", () => ({
  Tabs: (props: Record<string, unknown>) => props.children,
  TabsList: (props: Record<string, unknown>) => props.children,
  TabsTrigger: (props: Record<string, unknown>) => props.children,
  TabsContent: (props: Record<string, unknown>) => props.children,
}));

vi.mock("@tanstack/react-query", async () => {
  const actual =
    await vi.importActual<typeof import("@tanstack/react-query")>("@tanstack/react-query");
  return {
    ...actual,
    useQueryClient: () => ({ invalidateQueries: vi.fn() }),
  };
});

vi.mock("@/hooks/queries/admin/plugins", () => ({
  CHECK_PLUGIN_UPDATES_TASK_KEY: "check_plugin_updates",
  useAdminPlugins: () => useAdminPluginsMock(),
  useCheckPluginUpdates: () => ({ mutate: checkPluginUpdatesMutateMock, isPending: false }),
  useCreatePluginRepository: () => ({ mutate: vi.fn(), isPending: false }),
  useUpdatePluginRepository: () => ({ mutate: vi.fn(), isPending: false }),
  useDeletePluginRepository: () => ({ mutate: vi.fn(), isPending: false }),
  useInstallPlugin: () => ({ mutate: vi.fn(), isPending: false }),
  useUploadPlugin: () => ({ mutate: vi.fn(), isPending: false }),
  usePluginUpload: () => ({ upload: vi.fn(), progress: null, isPending: false }),
  useUpdatePluginInstallation: () => ({ mutate: vi.fn(), isPending: false }),
  useDeletePluginInstallation: () => ({ mutate: vi.fn(), isPending: false }),
  useSavePluginConfig: () => ({ mutate: vi.fn(), isPending: false }),
  useSavePluginAuthBinding: () => ({ mutate: vi.fn(), isPending: false }),
  useSavePluginTaskBinding: () => ({ mutate: vi.fn(), isPending: false }),
}));

vi.mock("@/hooks/queries/admin/tasks", () => ({
  useTask: () => ({ data: { key: "check_plugin_updates", state: "idle" } }),
}));

describe("AdminPlugins", () => {
  beforeEach(() => {
    capturedButtonProps.length = 0;
    checkPluginUpdatesMutateMock.mockReset();
    useAdminPluginsMock.mockReturnValue({
      repositories: [],
      catalog: [],
      installations: [],
      isLoading: false,
    });
  });

  it("starts the shared plugin update check task from the plugins page", () => {
    renderToStaticMarkup(
      <MemoryRouter>
        <AdminPlugins />
      </MemoryRouter>,
    );

    const button = capturedButtonProps.find((props) => {
      const children = props.children;
      if (typeof children === "string") {
        return children === "Check for updates";
      }
      return Array.isArray(children) && children.some((child) => child === "Check for updates");
    });

    expect(button).toBeTruthy();
    expect(typeof button?.onClick).toBe("function");

    (button?.onClick as () => void)();

    expect(checkPluginUpdatesMutateMock).toHaveBeenCalledTimes(1);
  });

  it("describes manual upload as a generic plugin file instead of a zip-only archive", () => {
    const markup = renderToStaticMarkup(
      <MemoryRouter>
        <AdminPlugins />
      </MemoryRouter>,
    );

    expect(markup).toContain("Upload a plugin binary directly.");
    expect(markup).toContain("Choose plugin file...");
    expect(markup).not.toContain('accept=".zip"');
  });
});
