import type { PluginAdminForm } from "@/api/types";
import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { SchemaForm } from "./SchemaForm";
import { field } from "./adminFormField.test-support";

const descriptor: PluginAdminForm = {
  fields: [
    field("service_kind", "Service", "SELECT", {
      required: true,
      options: [
        { value: "radarr", label: "Radarr" },
        { value: "sonarr", label: "Sonarr" },
      ],
    }),
    field("season_folder", "Season folder", "SWITCH", {
      show_when: [{ field: "service_kind", equals: ["sonarr"] }],
    }),
    field("root_folder", "Root folder", "SELECT", { dynamic_options: true }),
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
  it("renders a declared default_value when the field is absent from values (#6)", () => {
    const d: PluginAdminForm = {
      fields: [field("season_folder", "Season folder", "SWITCH", { default_value: true })],
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
        field("advanced_enabled", "Advanced", "SWITCH", { default_value: true }),
        field("endpoint", "Endpoint", "TEXT", {
          show_when: [{ field: "advanced_enabled", equals: ["true"] }],
        }),
      ],
    };
    render(<SchemaForm descriptor={d} values={{}} onChange={vi.fn()} />);
    expect(screen.getByText("Endpoint")).toBeTruthy();
  });
  it("reports validity through onValidityChange (#14)", () => {
    const onValidityChange = vi.fn();
    const d: PluginAdminForm = {
      fields: [field("name", "Name", "TEXT", { required: true })],
    };
    const { rerender } = render(
      <SchemaForm
        descriptor={d}
        values={{}}
        onChange={vi.fn()}
        onValidityChange={onValidityChange}
      />,
    );
    expect(onValidityChange).toHaveBeenCalledExactlyOnceWith(false);
    const replacementCallback = vi.fn();
    rerender(
      <SchemaForm
        descriptor={d}
        values={{}}
        onChange={vi.fn()}
        onValidityChange={replacementCallback}
      />,
    );
    expect(onValidityChange).toHaveBeenCalledTimes(1);
    expect(replacementCallback).not.toHaveBeenCalled();
    rerender(
      <SchemaForm
        descriptor={d}
        values={{ name: "ok" }}
        onChange={vi.fn()}
        onValidityChange={replacementCallback}
      />,
    );
    expect(replacementCallback).toHaveBeenCalledExactlyOnceWith(true);
    expect(onValidityChange).toHaveBeenCalledTimes(1);
  });
});

const collapsibleDescriptor: PluginAdminForm = {
  fields: [
    field("api_path", "API path", "TEXT", { required: true }),
    field("verbose", "Verbose", "SWITCH"),
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

  it("uses a controlling field default when rendering a conditional section", () => {
    const d: PluginAdminForm = {
      fields: [
        field("advanced_enabled", "Advanced", "SWITCH", { default_value: true }),
        field("endpoint", "Endpoint", "TEXT"),
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

describe("SchemaForm host-owned fields", () => {
  const hostDescriptor: PluginAdminForm = {
    fields: [
      field("quality_profile_id", "Quality profile", "SELECT", {
        required: true,
        options: [{ value: "1", label: "HD-1080p" }],
      }),
      field("is_default", "Default (HD/1080p)", "SWITCH"),
      field("anime_enabled", "Enable anime overrides", "SWITCH"),
      field("anime_root_folder", "Anime root folder", "TEXT", { required: true }),
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
          fields: [field("name", "Name", "TEXT"), hostDescriptor.fields[1]!],
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
});

describe("SchemaForm empty-string select options", () => {
  const promptForm: PluginAdminForm = {
    fields: [
      field("prompt", "Prompt", "SELECT", {
        default_value: "",
        options: [
          { value: "", label: "Provider default" },
          { value: "login", label: "Always ask for credentials" },
        ],
      }),
    ],
  };

  it("shows and writes back an option whose value is empty", async () => {
    Object.defineProperties(Element.prototype, {
      hasPointerCapture: { configurable: true, value: () => false },
      setPointerCapture: { configurable: true, value: () => {} },
      releasePointerCapture: { configurable: true, value: () => {} },
      scrollIntoView: { configurable: true, value: () => {} },
    });
    const onChange = vi.fn();
    const { rerender } = render(
      <SchemaForm descriptor={promptForm} values={{ prompt: "" }} onChange={onChange} />,
    );
    expect(screen.getByRole("combobox")).toHaveTextContent("Provider default");

    rerender(
      <SchemaForm descriptor={promptForm} values={{ prompt: "login" }} onChange={onChange} />,
    );
    expect(screen.getByRole("combobox")).toHaveTextContent("Always ask for credentials");
    fireEvent.pointerDown(screen.getByRole("combobox"), {
      button: 0,
      ctrlKey: false,
      pointerType: "mouse",
    });
    fireEvent.click(await screen.findByRole("option", { name: "Provider default" }));
    expect(onChange).toHaveBeenLastCalledWith({ prompt: "" });
  });
});
