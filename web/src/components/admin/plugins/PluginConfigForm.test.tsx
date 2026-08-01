// @vitest-environment jsdom

import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import type { PluginConfigSchema } from "@/api/types";

import { PluginConfigForm } from "./PluginConfigForm";

const schema: PluginConfigSchema = {
  key: "account",
  title: "Account",
  json_schema: "{}",
  required: true,
  admin_form: {
    fields: [
      {
        key: "api_key",
        label: "API Key",
        control: "PASSWORD",
        required: false,
        secret: true,
        multiline: false,
      },
      {
        key: "region",
        label: "Region",
        control: "TEXT",
        required: false,
        secret: false,
        multiline: false,
      },
    ],
  },
};

describe("PluginConfigForm secrets", () => {
  it("shows redacted saved state and only clears through an explicit action", async () => {
    const onSave = vi.fn();
    render(
      <PluginConfigForm
        schema={schema}
        value={{ region: "us-east" }}
        configuredSecrets={["api_key"]}
        onSave={onSave}
      />,
    );

    expect(screen.getByLabelText("API Key")).toHaveAttribute(
      "placeholder",
      "Saved secret — leave blank to keep",
    );
    expect(screen.getByText("API Key: saved")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Save config" }));
    expect(onSave).toHaveBeenLastCalledWith(
      "account",
      expect.objectContaining({ region: "us-east" }),
      [],
    );

    await userEvent.click(screen.getByRole("button", { name: "Clear saved secret" }));
    await userEvent.click(screen.getByRole("button", { name: "Save config" }));
    expect(onSave).toHaveBeenLastCalledWith(
      "account",
      expect.objectContaining({ region: "us-east" }),
      ["api_key"],
    );
  });

  it("does not offer to clear a required saved secret into an invalid config", () => {
    const requiredSchema: PluginConfigSchema = {
      ...schema,
      admin_form: {
        ...schema.admin_form!,
        fields: schema.admin_form!.fields.map((field) =>
          field.key === "api_key" ? { ...field, required: true } : field,
        ),
      },
    };
    render(
      <PluginConfigForm
        schema={requiredSchema}
        value={{ region: "us-east" }}
        configuredSecrets={["api_key"]}
        onSave={vi.fn()}
      />,
    );

    expect(screen.getByText("API Key: saved (required)")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Clear saved secret" })).not.toBeInTheDocument();
  });

  it("keeps the submitted snapshot immutable while a save is pending", () => {
    render(
      <PluginConfigForm
        schema={schema}
        value={{ region: "us-east" }}
        configuredSecrets={["api_key"]}
        onSave={vi.fn()}
        isSaving
      />,
    );

    expect(screen.getByLabelText("Region")).toBeDisabled();
    expect(screen.getByRole("button", { name: "Save config" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Clear saved secret" })).toBeDisabled();
  });

  it("tests the exact draft including staged secret removals", async () => {
    const onTest = vi.fn().mockResolvedValue({
      success: false,
      message: "API key is required",
    });
    render(
      <PluginConfigForm
        schema={schema}
        value={{ region: "us-east" }}
        configuredSecrets={["api_key"]}
        onSave={vi.fn()}
        onTest={onTest}
      />,
    );

    await userEvent.click(screen.getByRole("button", { name: "Clear saved secret" }));
    await userEvent.click(screen.getByRole("button", { name: "Check Connection" }));

    expect(onTest).toHaveBeenCalledWith("account", expect.objectContaining({ region: "us-east" }), [
      "api_key",
    ]);
  });
});

describe("PluginConfigForm quality profiles", () => {
  it("accepts Go/RE2 inline flags without applying JavaScript regex rules", async () => {
    const qualitySchema: PluginConfigSchema = {
      key: "streaming",
      title: "Streaming",
      json_schema: JSON.stringify({
        type: "object",
        properties: {
          quality_profiles: {
            type: "array",
            items: { type: "object" },
          },
        },
      }),
      required: true,
      admin_form: {
        fields: [
          {
            key: "quality_profiles",
            label: "Quality Profiles",
            control: "TEXTAREA",
            required: false,
            secret: false,
            multiline: true,
          },
        ],
      },
    };
    render(
      <PluginConfigForm
        schema={qualitySchema}
        value={{
          quality_profiles:
            '[{"label":"4K HDR","include_regex":"(?i)(2160p|4k)","exclude_regex":"(?i)(cam|ts)"}]',
        }}
        onSave={vi.fn()}
      />,
    );

    await userEvent.click(screen.getByRole("button", { name: "Validate profiles" }));

    expect(screen.getByText(/Valid JSON structure: 4K HDR/)).toBeInTheDocument();
    expect(screen.queryByText(/Invalid include_regex/)).not.toBeInTheDocument();
  });
});
