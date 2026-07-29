import { useCallback, useEffect, useMemo, useState } from "react";
import { createPortal } from "react-dom";
import { useNavigate } from "react-router";
import {
  Bell,
  CalendarDays,
  ExternalLink,
  Heart,
  MonitorSmartphone,
  Play,
  Plug,
  Sparkles,
  Subtitles,
  Users,
  Wand2,
} from "lucide-react";
import type { LucideIcon } from "lucide-react";
import type { OnboardingFlow, OnboardingStep, OnboardingStepLink } from "@/api/types";
import { getProfileToken } from "@/api/client";
import { Button } from "@/components/ui/button";
import { useAuth } from "@/hooks/useAuth";
import { useOnboardingProgress } from "@/hooks/queries/onboarding";
import { useUpdateProfile } from "@/hooks/queries/profiles";
import { useSetSetting } from "@/hooks/queries/settings";
import { cn } from "@/lib/utils";

/**
 * Renders the server-driven first-run tour as a modal overlay. The step
 * list comes from /onboarding/flow (already filtered per server and
 * profile); this component renders the kinds it knows and silently skips
 * anything else — that skip is the forward-compatibility contract.
 */

// Step kinds this client understands. A manifest step whose kind isn't
// listed here is dropped at render time, never an error.
const KNOWN_KINDS = new Set(["welcome", "feature_card", "setting_choice", "handoff"]);

const ILLUSTRATIONS: Record<string, LucideIcon> = {
  welcome: Sparkles,
  watchlist: Heart,
  "watch-together": Users,
  requests: Wand2,
  recommendations: Sparkles,
  calendar: CalendarDays,
  playback: Play,
  subtitles: Subtitles,
  notifications: Bell,
  apps: MonitorSmartphone,
  jellyfin: Plug,
};

interface TourHostProps {
  flow: OnboardingFlow;
  onDone: () => void;
}

export function TourHost({ flow, onDone }: TourHostProps) {
  const steps = useMemo(() => flow.steps.filter((s) => KNOWN_KINDS.has(s.kind)), [flow.steps]);
  const [index, setIndex] = useState(0);
  const progress = useOnboardingProgress();
  const navigate = useNavigate();

  const step = steps[index];

  const finish = useCallback(
    (opts: { skipped: boolean; route?: string }) => {
      progress.mutate({
        tour_id: flow.tour_id,
        last_step: step?.id ?? "",
        completed: !opts.skipped,
        skipped: opts.skipped,
      });
      onDone();
      if (opts.route) {
        navigate(opts.route);
      }
    },
    [progress, flow.tour_id, step, onDone, navigate],
  );

  const advance = useCallback(() => {
    const nextStep = steps[index + 1];
    if (!nextStep) {
      finish({ skipped: false });
      return;
    }
    progress.mutate({ tour_id: flow.tour_id, last_step: nextStep.id });
    setIndex(index + 1);
  }, [index, steps, finish, progress, flow.tour_id]);

  // Server sent nothing we can render — mark done (in an effect, not during
  // render) so we never loop back into an empty tour.
  useEffect(() => {
    if (!step) {
      finish({ skipped: false });
    }
    // finish is stable enough for this one-shot; re-running on step change is the point.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [step]);

  if (!step) {
    return null;
  }

  const isLast = index === steps.length - 1;

  // Portaled to <body>: an ancestor in the app layout creates a fixed-position
  // containing block, which would pin inset-0 to the content pane and leave
  // the sidebar un-scrimmed and un-blurred.
  return createPortal(
    <div
      className="fixed inset-0 z-[70] flex items-center justify-center bg-black/70 p-4 backdrop-blur-xl"
      role="dialog"
      aria-modal="true"
      aria-label="Feature tour"
    >
      <div className="bg-card border-border max-h-[85dvh] w-full max-w-lg overflow-y-auto rounded-2xl border p-6 shadow-2xl sm:p-7">
        <StepBody step={step} />

        <div className="mt-7 flex flex-wrap items-center justify-between gap-3">
          <Button
            variant="ghost"
            size="sm"
            onClick={() => finish({ skipped: true })}
            className="text-muted-foreground shrink-0"
          >
            Skip tour
          </Button>
          <div className="flex min-w-0 items-center gap-4">
            {/* Pips are decorative; on phones they'd crowd the buttons out. */}
            <div className="hidden items-center gap-1.5 sm:flex" aria-hidden="true">
              {steps.map((s, i) => (
                <span
                  key={s.id}
                  className={cn(
                    "h-1.5 rounded-full transition-all",
                    i === index ? "bg-primary w-4" : "bg-secondary w-1.5",
                  )}
                />
              ))}
            </div>
            <div className="flex shrink-0 gap-2">
              {index > 0 && (
                <Button variant="outline" size="sm" onClick={() => setIndex(index - 1)}>
                  Back
                </Button>
              )}
              {step.kind === "handoff" ? (
                <Button size="sm" onClick={() => finish({ skipped: false, route: step.route })}>
                  {step.title ?? "Finish"}
                </Button>
              ) : (
                <Button size="sm" onClick={advance}>
                  {isLast ? "Done" : index === 0 ? "Show me" : "Next"}
                </Button>
              )}
            </div>
          </div>
        </div>
      </div>
    </div>,
    document.body,
  );
}

function StepBody({ step }: { step: OnboardingStep }) {
  const Icon = ILLUSTRATIONS[step.illustration ?? ""] ?? Sparkles;

  return (
    <div>
      <div className="bg-secondary mb-5 grid size-12 place-items-center rounded-xl">
        <Icon className="text-foreground size-6" aria-hidden="true" />
      </div>
      {step.title && <h2 className="text-xl font-bold tracking-tight sm:text-2xl">{step.title}</h2>}
      {step.body && (
        <p className="text-muted-foreground mt-2.5 text-sm leading-relaxed">{step.body}</p>
      )}
      {step.kind === "setting_choice" && step.setting && (
        <div className="border-border bg-popover mt-5 rounded-xl border p-4">
          <SettingControl step={step} />
        </div>
      )}
      {step.links && step.links.length > 0 && (
        <div className="mt-5 flex flex-col gap-2 sm:flex-row sm:flex-wrap">
          {step.links.map((link) => (
            <StoreLink key={link.url} link={link} />
          ))}
        </div>
      )}
    </div>
  );
}

/**
 * Store links render as badge-style buttons with the store's brand mark,
 * inferred from the URL host so the server contract stays icon-free. Other
 * links fall back to a plain external-link button.
 */
function StoreLink({ link }: { link: OnboardingStepLink }) {
  const brand = link.url.includes("apple.com")
    ? {
        Icon: AppleLogo,
        eyebrow: link.url.includes("testflight") ? "TestFlight beta" : "App Store",
      }
    : link.url.includes("play.google.com")
      ? { Icon: GooglePlayLogo, eyebrow: "Google Play" }
      : null;

  if (!brand) {
    return (
      <Button variant="outline" size="sm" asChild className="max-w-full">
        <a href={link.url} target="_blank" rel="noopener noreferrer">
          <span className="truncate">{link.label}</span>
          <ExternalLink className="ml-1.5 size-3.5 shrink-0" aria-hidden="true" />
        </a>
      </Button>
    );
  }

  return (
    <a
      href={link.url}
      target="_blank"
      rel="noopener noreferrer"
      className="border-border bg-popover hover:bg-secondary focus-visible:ring-ring flex max-w-full items-center gap-3 rounded-xl border px-4 py-2.5 transition-colors focus-visible:ring-2 focus-visible:outline-none"
    >
      <brand.Icon className="size-6 shrink-0" aria-hidden="true" />
      <span className="min-w-0">
        <span className="text-muted-foreground block text-[10px] leading-tight tracking-wide uppercase">
          {brand.eyebrow}
        </span>
        <span className="block truncate text-sm leading-tight font-semibold">{link.label}</span>
      </span>
    </a>
  );
}

/** Apple logo mark (brand shape, rendered in the current text color). */
function AppleLogo({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 24 24" fill="currentColor" className={className} aria-hidden="true">
      <path d="M17.05 20.28c-.98.95-2.05.8-3.08.35-1.09-.46-2.09-.48-3.24 0-1.44.62-2.2.44-3.06-.35C2.79 15.25 3.51 7.59 9.05 7.31c1.35.07 2.29.74 3.08.8 1.18-.24 2.31-.93 3.57-.84 1.51.12 2.65.72 3.4 1.8-3.12 1.87-2.38 5.98.48 7.13-.57 1.5-1.31 2.99-2.54 4.09zM12.03 7.25c-.15-2.23 1.66-4.07 3.74-4.25.29 2.58-2.34 4.5-3.74 4.25z" />
    </svg>
  );
}

/** Google Play logo mark (brand shape, rendered in the current text color). */
function GooglePlayLogo({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 24 24" fill="currentColor" className={className} aria-hidden="true">
      <path d="M3.61 1.81c-.36.38-.57.97-.57 1.73v16.92c0 .76.21 1.35.57 1.73l.09.08 9.48-9.48v-.22L3.7 1.72l-.09.09z" />
      <path d="M16.34 15.95l-3.16-3.16v-.22l3.16-3.16.07.04 3.74 2.13c1.07.6 1.07 1.6 0 2.21l-3.74 2.12-.07.04z" />
      <path d="M16.41 15.91l-3.23-3.23-9.57 9.57c.35.37 .93.42 1.59.05l11.21-6.39z" />
      <path d="M16.41 8.45L5.2 2.07c-.66-.38-1.24-.33-1.59.05l9.57 9.56 3.23-3.23z" />
    </svg>
  );
}

/**
 * A setting_choice writes through the same APIs the settings screens use,
 * dispatched on the spec's target. Saves are immediate — by the end of the
 * tour the account is genuinely configured.
 */
function SettingControl({ step }: { step: OnboardingStep }) {
  const spec = step.setting!;
  const { profile, selectProfile } = useAuth();
  const updateProfile = useUpdateProfile();
  const setSetting = useSetSetting();

  const currentValue =
    spec.target === "profile_field" && profile
      ? ((profile as unknown as Record<string, unknown>)[spec.key] as string | undefined)
      : undefined;
  const [value, setValue] = useState<string>(currentValue || spec.default || "");

  function save(next: string) {
    setValue(next);
    if (spec.target === "profile_field") {
      if (!profile) return;
      updateProfile.mutate(
        { id: profile.id, body: { [spec.key]: next } },
        {
          onSuccess: (updated) => selectProfile(updated, getProfileToken() ?? undefined),
        },
      );
      return;
    }
    // "setting" and "device_setting" share the user-settings mutation shape;
    // device scope only differs in endpoint, handled by the settings hook.
    setSetting.mutate({ key: spec.key, value: next });
  }

  if (spec.control === "segmented") {
    return (
      <div className="bg-secondary inline-flex gap-1 rounded-lg p-1">
        {(spec.options ?? []).map((opt) => (
          <button
            key={opt.value}
            type="button"
            onClick={() => save(opt.value)}
            className={cn(
              "rounded-md px-3.5 py-1.5 text-sm font-medium transition-colors",
              value === opt.value
                ? "bg-card text-foreground shadow-sm"
                : "text-muted-foreground hover:text-foreground",
            )}
          >
            {opt.label}
          </button>
        ))}
      </div>
    );
  }

  // "select" (and any unrecognized control) renders as a radio-style list —
  // full labels matter more than compactness inside the tour.
  return (
    <div className="grid gap-1.5">
      {(spec.options ?? []).map((opt) => (
        <button
          key={opt.value}
          type="button"
          onClick={() => save(opt.value)}
          className={cn(
            "flex items-center gap-3 rounded-lg px-3 py-2 text-left text-sm transition-colors",
            value === opt.value ? "bg-secondary font-medium" : "hover:bg-secondary/50",
          )}
        >
          <span
            className={cn(
              "grid size-4 shrink-0 place-items-center rounded-full border",
              value === opt.value ? "border-primary" : "border-border",
            )}
            aria-hidden="true"
          >
            {value === opt.value && <span className="bg-primary size-2 rounded-full" />}
          </span>
          {opt.label}
        </button>
      ))}
    </div>
  );
}
