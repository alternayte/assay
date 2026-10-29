import { AlertTriangle, CheckCircle2, Info } from "lucide-react";
import type * as React from "react";
import { cn } from "@/lib/cn";

const tones = {
  info: { box: "border-accent bg-accent-wash text-ink", icon: Info },
  success: { box: "border-supported bg-wash-supported text-ink", icon: CheckCircle2 },
  danger: { box: "border-danger bg-wash-danger text-ink", icon: AlertTriangle },
} as const;

/** A message with an icon and text, so it never depends on colour alone. */
export function Notice({
  tone = "info",
  children,
  className,
}: {
  tone?: keyof typeof tones;
  children: React.ReactNode;
  className?: string;
}) {
  const { box, icon: Icon } = tones[tone];
  return (
    <div
      role={tone === "danger" ? "alert" : "status"}
      className={cn("flex items-start gap-2 rounded-sm border-l-4 px-3 py-2 text-base", box, className)}
    >
      <Icon aria-hidden className="mt-1 size-4 shrink-0" />
      <div>{children}</div>
    </div>
  );
}
