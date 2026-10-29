import { cn } from "@/lib/cn";

export function Skeleton({ className }: { className?: string }) {
  return <div aria-hidden className={cn("animate-pulse rounded-sm bg-paper-sunk", className)} />;
}

/** Rows of skeleton lines on the ruled rhythm. */
export function SkeletonRows({ rows = 4, label }: { rows?: number; label: string }) {
  return (
    <div role="status" aria-label={label} className="flex flex-col gap-3 py-2">
      {Array.from({ length: rows }, (_, i) => (
        // The rows have no identity. The index is the key.
        // biome-ignore lint/suspicious/noArrayIndexKey: static placeholder rows
        <Skeleton key={i} className="h-6 w-full" />
      ))}
    </div>
  );
}
