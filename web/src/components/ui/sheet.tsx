import type * as React from "react";
import { cn } from "@/lib/cn";

/** A section of the register: a heading, an optional description and content. */
export function Sheet({
  title,
  description,
  actions,
  children,
  className,
}: {
  title: string;
  description?: string;
  actions?: React.ReactNode;
  children: React.ReactNode;
  className?: string;
}) {
  const id = title.toLowerCase().replaceAll(/[^a-z0-9]+/g, "-");
  return (
    <section
      aria-labelledby={id}
      className={cn("rounded-md border border-rule bg-paper-raised shadow-sheet", className)}
    >
      <header className="flex flex-wrap items-end justify-between gap-3 border-b border-rule px-4 py-3">
        <div>
          <h2 id={id} className="text-lg font-semibold">
            {title}
          </h2>
          {description && <p className="text-base text-ink-muted">{description}</p>}
        </div>
        {actions && <div className="flex flex-wrap gap-2">{actions}</div>}
      </header>
      <div className="px-4 py-3">{children}</div>
    </section>
  );
}

/** An empty list. It names the next action. */
export function Empty({ children, action }: { children: React.ReactNode; action?: React.ReactNode }) {
  return (
    <div className="flex flex-col items-start gap-3 py-6 text-base text-ink-muted">
      <p>{children}</p>
      {action}
    </div>
  );
}
