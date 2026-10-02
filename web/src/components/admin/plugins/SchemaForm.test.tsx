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
  it("uses a controlling field default when rendering a conditional field", () => {
    const d: PluginAdminForm = {
      fields: [
        {
          key: "advanced_enabled",
          label: "Advanced",
          control: "SWITCH",
          required: false,
          secret: false,
          multiline: false,
          default_value: true,
        },
        {
          key: "endpoint",
          label: "Endpoint",
          control: "TEXT",
          required: false,
          secret: false,
          multiline: false,
          show_when: [{ field: "advanced_enabled", equals: ["true"] }],
        },
      ],
    };
    render(<SchemaForm descriptor={d} values={{}} onChange={vi.fn()} />);
    expect(screen.getByText("Endpoint")).toBeTruthy();
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

  it("uses a controlling field default when rendering a conditional section", () => {
    const d: PluginAdminForm = {
      fields: [
        {
          key: "advanced_enabled",
          label: "Advanced",
          control: "SWITCH",
          required: false,
          secret: false,
          multiline: false,
          default_value: true,
        },
        {
          key: "endpoint",
          label: "Endpoint",
          control: "TEXT",
          required: false,
          secret: false,
          multiline: false,
        },
      ],
      sections: [
        {
          key: "advanced",
          title: "Advanced options",
          collapsible: false,
          collapsed_default: false,
          field_keys: ["endpoint"],
          show_when: [{ field: "advanced_enabled", equals: ["true"] }],
        },
      ],
    };
    render(<SchemaForm descriptor={d} values={{}} onChange={vi.fn()} />);
    expect(screen.getByText("Advanced options")).toBeTruthy();
    expect(screen.getByText("Endpoint")).toBeTruthy();
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

describe("SchemaForm host-owned fields", () => {
  const hostDescriptor: PluginAdminForm = {
    fields: [
      {
        key: "quality_profile_id",
        label: "Quality profile",
        control: "SELECT",
        required: true,
        secret: false,
        multiline: false,
        options: [{ value: "1", label: "HD-1080p" }],
      },
      {
        key: "is_default",
        label: "Default (HD/1080p)",
        control: "SWITCH",
        required: false,
        secret: false,
        multiline: false,
      },
      {
        key: "anime_enabled",
        label: "Enable anime overrides",
        control: "SWITCH",
        required: false,
        secret: false,
        multiline: false,
      },
      {
        key: "anime_root_folder",
        label: "Anime root folder",
        control: "TEXT",
        required: true,
        secret: false,
        multiline: false,
      },
    ],
    sections: [
      {
        key: "library",
        title: "Library",
        collapsible: true,
        collapsed_default: true,
        field_keys: ["quality_profile_id", "is_default"],
      },
      {
        key: "anime",
        title: "Anime overrides",
        collapsible: false,
        collapsed_default: false,
        field_keys: ["anime_enabled", "anime_root_folder"],
      },
    ],
  };
  const hidden = ["is_default", "anime_enabled", "anime_root_folder"];

  it("hides host-owned keys and drops a section they leave empty", () => {
    render(
      <SchemaForm
        descriptor={hostDescriptor}
        values={{ quality_profile_id: "1", is_default: true }}
        onChange={vi.fn()}
        hiddenKeys={hidden}
        expandSections
      />,
    );
    expect(screen.queryByText("Default (HD/1080p)")).toBeNull();
    expect(screen.queryByText("Anime overrides")).toBeNull();
    expect(screen.queryByText("Anime root folder")).toBeNull();
    expect(screen.getByText("Quality profile")).toBeTruthy();
  });

  it("keeps hidden values when a visible field changes", () => {
    const onChange = vi.fn();
    render(
      <SchemaForm
        descriptor={{
          fields: [
            {
              key: "name",
              label: "Name",
              control: "TEXT",
              required: false,
              secret: false,
              multiline: false,
            },
            hostDescriptor.fields[1]!,
          ],
        }}
        values={{ name: "a", is_default: true }}
        onChange={onChange}
        hiddenKeys={hidden}
      />,
    );
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "b" } });
    expect(onChange).toHaveBeenCalledWith({ name: "b", is_default: true });
  });

  it("leaves hidden required fields out of validation", () => {
    const onValidityChange = vi.fn();
    render(
      <SchemaForm
        descriptor={hostDescriptor}
        values={{ quality_profile_id: "1", anime_enabled: true }}
        onChange={vi.fn()}
        onValidityChange={onValidityChange}
        hiddenKeys={hidden}
      />,
    );
    expect(onValidityChange).toHaveBeenLastCalledWith(true);
  });

  it("renders collapsible sections open and without a toggle when expanded", () => {
    render(
      <SchemaForm
        descriptor={hostDescriptor}
        values={{ quality_profile_id: "1" }}
        onChange={vi.fn()}
        hiddenKeys={hidden}
        expandSections
      />,
    );
    expect(screen.queryByText("Show")).toBeNull();
    expect(screen.getByText("Quality profile")).toBeTruthy();
  });
});
