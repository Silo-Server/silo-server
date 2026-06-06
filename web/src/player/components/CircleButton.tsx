export type CircleButtonProps = {
  size: "sm" | "md" | "lg";
  variant: "primary" | "secondary";
  ariaLabel: string;
  onClick?: () => void;
  disabled?: boolean;
  children: React.ReactNode;
  "data-paused"?: boolean;
};

/**
 * Glass-disc button used in both the video and audiobook player transports.
 * - `primary` = glossy white disc (play/pause).
 * - `secondary` = subtle glass disc (skip, prev/next).
 * Sizes: `sm` 40–44px in-bar secondaries, `md` 52–56px in-bar play,
 * `lg` 80px for floating variants (e.g. Now Listening play button).
 */
export function CircleButton({
  size,
  variant,
  ariaLabel,
  onClick,
  disabled,
  children,
  "data-paused": dataPaused,
}: CircleButtonProps) {
  const base =
    "flex items-center justify-center rounded-full focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-white/75 disabled:opacity-40 disabled:cursor-not-allowed";
  const sizing =
    size === "lg"
      ? "h-20 w-20"
      : size === "md"
        ? "h-12 w-12 sm:h-14 sm:w-14"
        : "h-10 w-10 sm:h-11 sm:w-11";
  const skin = variant === "primary" ? "player-disc-primary" : "player-disc-secondary";
  return (
    <button
      type="button"
      aria-label={ariaLabel}
      onClick={onClick}
      disabled={disabled}
      className={`${base} ${sizing} ${skin}`}
      data-paused={dataPaused ? "true" : undefined}
    >
      {children}
    </button>
  );
}
