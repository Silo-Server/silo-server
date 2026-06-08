import { describe, expect, it, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import type { PluginAdminForm } from "@/api/types";
import { SchemaForm } from "./SchemaForm";

const descriptor: PluginAdminForm = {
  fields: [
    {
      key: "service_kind",
      label: "Service",
      control: "SELECT",
      required: true,
      secret: false,
      multiline: false,
      options: [
        { value: "radarr", label: "Radarr" },
        { value: "sonarr", label: "Sonarr" },
      ],
    },
    {
      key: "season_folder",
      label: "Season folder",
      control: "SWITCH",
      required: false,
      secret: false,
      multiline: false,
      show_when: [{ field: "service_kind", equals: ["sonarr"] }],
    },
    {
      key: "root_folder",
      label: "Root folder",
      control: "SELECT",
      required: false,
      secret: false,
      multiline: false,
      dynamic_options: true,
    },
  ],
  sections: [
    {
      key: "main",
      title: "Library",
      collapsible: false,
      collapsed_default: false,
      field_keys: ["service_kind", "season_folder", "root_folder"],
    },
  ],
};

function renderForm(
  values: Record<string, unknown>,
  extra: Partial<React.ComponentProps<typeof SchemaForm>> = {},
) {
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
  it("renders a declared default_value when the field is absent from values (#6)", () => {
    const d: PluginAdminForm = {
      fields: [
        {
          key: "season_folder",
          label: "Season folder",
          control: "SWITCH",
          required: false,
          secret: false,
          multiline: false,
          default_value: true,
        },
      ],
    };
    const onChange = vi.fn();
    render(<SchemaForm descriptor={d} values={{}} onChange={onChange} />);
    expect((screen.getByRole("switch") as HTMLButtonElement).getAttribute("aria-checked")).toBe(
      "true",
    );
  });
  it("reports validity through onValidityChange (#14)", () => {
    const onValidityChange = vi.fn();
    const d: PluginAdminForm = {
      fields: [
        {
          key: "name",
          label: "Name",
          control: "TEXT",
          required: true,
          secret: false,
          multiline: false,
        },
      ],
    };
    const { rerender } = render(
      <SchemaForm
        descriptor={d}
        values={{}}
        onChange={vi.fn()}
        onValidityChange={onValidityChange}
      />,
    );
    expect(onValidityChange).toHaveBeenLastCalledWith(false);
    rerender(
      <SchemaForm
        descriptor={d}
        values={{ name: "ok" }}
        onChange={vi.fn()}
        onValidityChange={onValidityChange}
      />,
    );
    expect(onValidityChange).toHaveBeenLastCalledWith(true);
  });
});

const collapsibleDescriptor: PluginAdminForm = {
  fields: [
    {
      key: "api_path",
      label: "API path",
      control: "TEXT",
      required: true,
      secret: false,
      multiline: false,
    },
    {
      key: "verbose",
      label: "Verbose",
      control: "SWITCH",
      required: false,
      secret: false,
      multiline: false,
    },
  ],
  sections: [
    {
      key: "lib",
      title: "Library",
      collapsible: true,
      collapsed_default: true,
      field_keys: ["api_path", "verbose"],
    },
  ],
};

describe("SchemaForm collapsible sections", () => {
  it("honors collapsed_default when the section has no field errors", () => {
    render(
      <SchemaForm
        descriptor={collapsibleDescriptor}
        values={{ api_path: "/v3" }}
        onChange={vi.fn()}
      />,
    );
    expect(screen.queryByText("Verbose")).toBeNull(); // collapsed -> field hidden
    expect(screen.getByText("Show")).toBeTruthy();
  });

  it("auto-expands a collapsed section that has a validation error (empty required field)", () => {
    render(<SchemaForm descriptor={collapsibleDescriptor} values={{}} onChange={vi.fn()} />);
    // api_path is required + empty -> validateSchemaValues flags it -> section force-expands
    expect(screen.getByText("Verbose")).toBeTruthy();
  });

  it("expands a clean collapsed section when Show is clicked", () => {
    render(
      <SchemaForm
        descriptor={collapsibleDescriptor}
        values={{ api_path: "/v3" }}
        onChange={vi.fn()}
      />,
    );
    fireEvent.click(screen.getByText("Show"));
    expect(screen.getByText("Verbose")).toBeTruthy();
  });
});

it("marks a show_when-gated field as nested when it is revealed", () => {
  const d: PluginAdminForm = {
    fields: [
      {
        key: "service_kind",
        label: "Service",
        control: "SELECT",
        required: false,
        secret: false,
        multiline: false,
        options: [{ value: "sonarr", label: "Sonarr" }],
      },
      {
        key: "series_type",
        label: "Series type",
        control: "SELECT",
        required: false,
        secret: false,
        multiline: false,
        show_when: [{ field: "service_kind", equals: ["sonarr"] }],
        options: [{ value: "standard", label: "Standard" }],
      },
    ],
  };
  const { container } = render(
    <SchemaForm descriptor={d} values={{ service_kind: "sonarr" }} onChange={vi.fn()} />,
  );
  expect(container.querySelector('[data-nested="true"]')).not.toBeNull();
});
