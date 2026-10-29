import { cva, type VariantProps } from "class-variance-authority";
import type * as React from "react";
import { cn } from "@/lib/cn";

const badgeVariants = cva(
  "inline-flex items-center gap-1 rounded-sm border px-1.5 font-mono text-xs leading-5 uppercase tracking-wide",
  {
    variants: {
      tone: {
        neutral: "border-rule-strong text-ink-muted",
        accent: "border-accent text-accent",
        supported: "border-supported bg-wash-supported text-supported",
        conflict: "border-conflict bg-wash-conflict text-conflict",
        stale: "border-stale bg-wash-stale text-stale",
        danger: "border-danger bg-wash-danger text-danger",
      },
    },
    defaultVariants: { tone: "neutral" },
  },
);

/** A status mark. It always carries text. */
export function Badge({
  className,
  tone,
  ...props
}: React.ComponentProps<"span"> & VariantProps<typeof badgeVariants>) {
  return <span className={cn(badgeVariants({ tone }), className)} {...props} />;
}
