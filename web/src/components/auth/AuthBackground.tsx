import { useBranding } from "@/hooks/useBranding";

/**
 * Full-bleed custom login background with a readability scrim. Renders nothing
 * when no login background image is configured, so it is safe to drop into any
 * auth page. Must be the first child of an `.auth-shell` element (which is
 * `position: relative; overflow: hidden`); it sits above the shell's gradient
 * ::before layer and below the `.auth-card` (z-index 1).
 */
export function AuthBackground() {
  const { loginBgUrl } = useBranding();
  if (!loginBgUrl) return null;

  return (
    <div aria-hidden className="pointer-events-none absolute inset-0 z-0">
      <img src={loginBgUrl} alt="" className="h-full w-full object-cover" />
      <div className="bg-background/70 absolute inset-0" />
    </div>
  );
}
