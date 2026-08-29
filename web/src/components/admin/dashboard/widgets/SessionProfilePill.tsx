export function SessionProfilePill({ label }: { label: string }) {
  return (
    <span className="border-primary/30 bg-primary/15 text-primary inline-flex max-w-full shrink-0 items-center rounded border px-1.5 py-0.5 align-middle text-[9px] leading-[1.1] font-semibold whitespace-nowrap">
      {label}
    </span>
  );
}
