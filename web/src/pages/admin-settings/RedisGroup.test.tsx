import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { useSettingsForm } from "@/hooks/useSettingsForm";

import { RedisGroup } from "./RedisGroup";
import { REDIS_KEYS } from "./redisDraft";

const { sensitiveStatus } = vi.hoisted(() => ({
  sensitiveStatus: {
    current: {} as {
      configured: string[];
      managed_by_env: string[];
      managed_by_env_vars?: Record<string, string>;
    },
  },
}));

vi.mock("@/hooks/queries/admin/settings", () => ({
  // The snapshot never carries the URL; whether one is saved comes from the
  // protected-credential status.
  // The server reports the number in use: 0 when the URL names none.
  useAdminServerSettings: () => ({ data: { "redis.db": "0" }, isLoading: false }),
  useAdminSensitiveStatus: () => ({ data: sensitiveStatus.current, isError: false }),
  useUpdateServerSettings: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useCheckAdminSettingsConnection: () => ({ mutateAsync: vi.fn(), isPending: false }),
}));

const OTHER_KEY = "server.public_url";

function Harness() {
  const form = useSettingsForm({ keys: [...REDIS_KEYS, OTHER_KEY] });
  return (
    <>
      <RedisGroup
        form={form}
        restartKeys={{ has: () => false }}
        secrets={{ keepSaved: form.resetValue, setSecret: form.setValue, disabled: false }}
      />
      {/* Another setting on the same page, so an edit can stay staged. */}
      <input
        aria-label="Other setting"
        onChange={(e) => form.setValue(OTHER_KEY, e.target.value)}
      />
      <p>{`${form.dirtyCount} staged`}</p>
    </>
  );
}

describe("RedisGroup", () => {
  beforeEach(() => {
    sensitiveStatus.current = { configured: [], managed_by_env: [] };
  });

  it("stages nothing when Use Redis goes on and back off with no saved URL", async () => {
    render(<Harness />);
    const useRedis = screen.getByRole("switch", { name: "Use Redis" });

    await userEvent.click(useRedis);
    await userEvent.type(screen.getByLabelText("Connection URL"), "redis://cache:6379");
    await userEvent.click(useRedis);

    expect(useRedis).not.toBeChecked();
    expect(screen.getByText("0 staged")).toBeInTheDocument();
  });

  it("shows the connection REDIS_URL sets once it takes over a staged switch-off", async () => {
    sensitiveStatus.current = { configured: ["redis.url"], managed_by_env: [] };
    const { rerender } = render(<Harness />);
    // An edit elsewhere on the page stays staged after the environment takes
    // the Redis keys over.
    await userEvent.type(screen.getByLabelText("Other setting"), "https://media.example.com");
    await userEvent.click(screen.getByRole("switch", { name: "Use Redis" }));
    expect(screen.queryByLabelText("Connection URL")).not.toBeInTheDocument();
    // The switch-off stages the URL and the number with it.
    expect(screen.getByText("3 staged")).toBeInTheDocument();

    // REDIS_URL sets both keys, as the server reports it.
    sensitiveStatus.current = {
      configured: ["redis.url"],
      managed_by_env: ["redis.url", "redis.db"],
      managed_by_env_vars: { "redis.url": "REDIS_URL", "redis.db": "REDIS_URL" },
    };
    rerender(<Harness />);

    expect(screen.getByRole("switch", { name: "Use Redis" })).toBeChecked();
    expect(screen.getByLabelText("Connection URL")).toBeInTheDocument();
    // The Redis drafts went with the takeover; the other edit stays.
    expect(screen.getByText("1 staged")).toBeInTheDocument();
  });
});
