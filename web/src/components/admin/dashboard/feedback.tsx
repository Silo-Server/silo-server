import { Skeleton } from "@/components/ui/skeleton";

export function SectionError({ message }: { message: string }) {
  return <div className="text-destructive py-4 text-center text-sm">{message}</div>;
}

export function LibrarySkeletonRows() {
  return (
    <>
      {Array.from({ length: 3 }).map((_, i) => (
        <Skeleton key={i} className="h-[60px] rounded-md" />
      ))}
    </>
  );
}

export function UserSkeletonRows() {
  return (
    <div className="space-y-2">
      {Array.from({ length: 4 }).map((_, i) => (
        <Skeleton key={i} className="h-10 rounded-md" />
      ))}
    </div>
  );
}

export function ActivitySkeletonRows() {
  return (
    <div className="space-y-0">
      {Array.from({ length: 4 }).map((_, i) => (
        <div key={i} className="border-border/30 flex items-start gap-3 border-b py-2.5">
          <Skeleton className="h-[30px] w-[30px] rounded-lg" />
          <div className="min-w-0 flex-1 space-y-1.5">
            <Skeleton className="h-3 w-3/4 rounded" />
            <Skeleton className="h-2 w-1/4 rounded" />
          </div>
        </div>
      ))}
    </div>
  );
}
