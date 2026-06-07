import { describe, expect, it, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import type { PluginAdminForm } from "@/api/types";
import { SchemaForm } from "./SchemaForm";

const descriptor: PluginAdminForm = {
  fields: [
    { key: "service_kind", label: "Service", control: "SELECT", required: true, secret: false, multiline: false,
      options: [{ value: "radarr", label: "Radarr" }, { value: "sonarr", label: "Sonarr" }] },
    { key: "season_folder", label: "Season folder", control: "SWITCH", required: false, secret: false, multiline: false,
      show_when: [{ field: "service_kind", equals: ["sonarr"] }] },
    { key: "root_folder", label: "Root folder", control: "SELECT", required: false, secret: false, multiline: false, dynamic_options: true },
  ],
  sections: [{ key: "main", title: "Library", collapsible: false, collapsed_default: false, field_keys: ["service_kind", "season_folder", "root_folder"] }],
};

function renderForm(values: Record<string, unknown>, extra: Partial<React.ComponentProps<typeof SchemaForm>> = {}) {
  const onChange = vi.fn();
  render(<SchemaForm descriptor={descriptor} values={values} onChange={onChange} {...extra} />);
  return { onChange };
}

describe("SchemaForm", () => {
  it("hides a field whose show_when is unmet", () => {
    renderForm({ service_kind: "radarr" });
    expect(screen.queryByText("Season folder")).toBeNull();
  });
  it("shows a field whose show_when is met", () => {
    renderForm({ service_kind: "sonarr" });
    expect(screen.getByText("Season folder")).toBeTruthy();
  });
  it("renders dynamic options for a dynamic_options select", () => {
    renderForm({}, { dynamicOptions: { root_folder: [{ value: "/movies", label: "/movies" }] } });
    expect(screen.getByText("Root folder")).toBeTruthy();
  });
  it("renders a server field error", () => {
    renderForm({ service_kind: "radarr" }, { errors: { service_kind: "bad service" } });
    expect(screen.getByText("bad service")).toBeTruthy();
  });
  it("emits onChange when a switch toggles", () => {
    const { onChange } = renderForm({ service_kind: "sonarr", season_folder: false });
    fireEvent.click(screen.getByRole("switch"));
    expect(onChange).toHaveBeenCalled();
  });
});
