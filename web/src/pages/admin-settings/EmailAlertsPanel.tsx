import { useEffect, useRef } from "react";

import { v2 } from "@/api/v2/request";

import pageCSS from "./emailAlertsPage/page.css?raw";
import pageHTML from "./emailAlertsPage/page.html?raw";
import { mountEmailAlertsPage, type EmailAlertsPageAPI } from "./emailAlertsPage/page.js";

type Overview = Awaited<ReturnType<typeof readOverview>>;
type Report = NonNullable<Overview["report"]>;

function readOverview() {
  return v2("GET /api/v2/admin/notifications/email-alerts");
}

// The page was written against the plugin's admin API; these map the
// server's responses onto the shapes it reads.
function pluginReport(r: Report) {
  return {
    time: r.time,
    reason: r.reason,
    emails_sent: r.sent,
    recipients_due: r.recipients,
    items_found: r.items,
    skipped: r.skipped,
    errors: r.errors,
  };
}

function pluginState(o: Overview) {
  const timezone = o.settings.schedule.timezone;
  let now = new Date(o.now).toLocaleString(undefined, {
    weekday: "short",
    day: "numeric",
    month: "short",
    hour: "2-digit",
    minute: "2-digit",
    timeZoneName: "short",
  });
  try {
    if (timezone) {
      now = new Date(o.now).toLocaleString(undefined, {
        timeZone: timezone,
        weekday: "short",
        day: "numeric",
        month: "short",
        hour: "2-digit",
        minute: "2-digit",
        timeZoneName: "short",
      });
    }
  } catch {
    // An unknown zone shows the browser's time.
  }
  return {
    settings: o.settings,
    defaults: o.defaults,
    recipients: o.recipients.map((r) => ({
      ...r,
      id: r.key,
      source: r.kind === "profile" ? "user" : "admin",
      libraries: (r.library_ids ?? []).map(String),
    })),
    libraries: o.libraries.map((l) => ({ ...l, id: String(l.id) })),
    max_age_options: o.max_age_options,
    problems: o.problems,
    report: o.report ? pluginReport(o.report) : null,
    log: o.log.map((l) => ({
      time: l.sent_at,
      recipient: l.email,
      mode: l.leg,
      items: l.items,
      subject: l.subject,
      error: l.error,
    })),
    server: {
      name: o.server_name,
      smtp_host: o.smtp_host,
      from: o.from_address,
      timezone: timezone || "the server's time zone",
      now,
    },
  };
}

interface PluginRecipient {
  id?: string;
  name?: string;
  email?: string;
  mode: "instant" | "instant_daily" | "instant_weekly" | "daily" | "weekly";
  enabled: boolean;
  max_age_hours?: number;
  types?: string[];
  libraries?: string[];
}

/**
 * The email alerts plugin's admin page, unchanged in look and behaviour,
 * mounted in a shadow root so its styles neither leak nor inherit.
 */
export default function EmailAlertsPanel() {
  const hostRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const host = hostRef.current;
    if (!host) return;
    // Silo has one dark theme.
    host.dataset.theme = "dark";
    const root = host.shadowRoot ?? host.attachShadow({ mode: "open" });
    root.innerHTML = `<style>${pageCSS}</style><div class="ea-body">${pageHTML}</div>`;

    // Profile recipients keep their "what to send" choice, which the page
    // does not show.
    const contentByKey = new Map<string, "followed" | "all">();
    const remember = (o: Overview) => {
      for (const r of o.recipients) contentByKey.set(r.key, r.content);
      return pluginState(o);
    };

    const api: EmailAlertsPageAPI = async (path, opts = {}) => {
      const body = opts.body as Record<string, unknown> | undefined;
      if (path === "state") return remember(await readOverview());
      if (path === "settings") {
        return remember(
          await v2("PUT /api/v2/admin/notifications/email-alerts/settings", {
            body: body as { settings: Overview["settings"] },
          }),
        );
      }
      if (path === "preview") {
        return v2("POST /api/v2/admin/notifications/email-alerts/preview", {
          body: body as { settings: Overview["settings"]; mode?: "instant" | "daily" | "weekly" },
        });
      }
      if (path === "test") {
        const { email, mode, pick, settings } = body as {
          email: string;
          mode?: "instant" | "daily" | "weekly";
          pick?: string;
          settings?: Overview["settings"];
        };
        return v2("POST /api/v2/admin/notifications/email-alerts/test", {
          body: {
            email,
            mode,
            settings,
            pick: pick === "episode" || pick === "movie" ? pick : undefined,
          },
          retryAuthentication: false,
        });
      }
      if (path === "run") {
        return pluginReport(
          await v2("POST /api/v2/admin/notifications/email-alerts/run", {
            retryAuthentication: false,
          }),
        );
      }
      if (path === "recipients") {
        const r = body as unknown as PluginRecipient;
        const recipient = {
          name: r.name,
          email: r.email,
          mode: r.mode,
          enabled: r.enabled,
          max_age_hours: r.max_age_hours,
          types: r.types,
          library_ids: (r.libraries ?? []).map(Number),
          content: r.id ? contentByKey.get(r.id) : undefined,
        };
        if (r.id) {
          return v2("PUT /api/v2/admin/notifications/email-alerts/recipients/{key}", {
            path: { key: r.id },
            body: recipient,
          });
        }
        return v2("POST /api/v2/admin/notifications/email-alerts/recipients", {
          body: recipient,
          retryAuthentication: false,
        });
      }
      const rewind = /^recipients\/(.+)\/rewind$/.exec(path);
      if (rewind?.[1]) {
        return v2("POST /api/v2/admin/notifications/email-alerts/recipients/{key}/rewind", {
          path: { key: decodeURIComponent(rewind[1]) },
          body: { hours: Number((body as { hours?: number } | undefined)?.hours ?? 24) },
          retryAuthentication: false,
        });
      }
      const recipient = /^recipients\/(.+)$/.exec(path);
      if (recipient?.[1] && opts.method === "DELETE") {
        return v2("DELETE /api/v2/admin/notifications/email-alerts/recipients/{key}", {
          path: { key: decodeURIComponent(recipient[1]) },
        });
      }
      throw new Error(`Unknown request ${opts.method ?? "GET"} ${path}`);
    };

    const unmount = mountEmailAlertsPage(root, api);
    return () => {
      unmount();
      root.innerHTML = "";
    };
  }, []);

  return <div ref={hostRef} className="overflow-hidden rounded-xl" />;
}
