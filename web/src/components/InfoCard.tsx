interface InfoCardProps {
  label: string;
  value: string;
}

export default function InfoCard({ label, value }: InfoCardProps) {
  return (
    <div className="bg-surface border-border hover:bg-surface-hover flex min-w-[130px] flex-1 flex-col gap-1 rounded-lg border px-5 py-4 transition-colors duration-150">
      <span className="text-muted-foreground text-[0.6875rem] font-medium">{label}</span>
      <span className="text-[0.9375rem] font-semibold">{value}</span>
    </div>
  );
}
