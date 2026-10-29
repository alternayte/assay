import { X } from "lucide-react";
import { Dialog as D } from "radix-ui";
import type * as React from "react";
import { cn } from "@/lib/cn";

export const Dialog = D.Root;
export const DialogTrigger = D.Trigger;
export const DialogClose = D.Close;

export function DialogContent({
  title,
  description,
  children,
  className,
  closeLabel,
}: {
  title: string;
  description?: string;
  children: React.ReactNode;
  className?: string;
  closeLabel: string;
}) {
  return (
    <D.Portal>
      <D.Overlay className="fixed inset-0 z-40 bg-ink/30 dark:bg-paper-sunk/70" />
      <D.Content
        className={cn(
          "fixed top-[12vh] left-1/2 z-50 flex max-h-[80vh] w-[calc(100vw-2rem)] max-w-lg -translate-x-1/2 flex-col gap-4 overflow-y-auto rounded-md border border-rule-strong bg-paper-raised p-5 shadow-sheet",
          className,
        )}
      >
        <div className="flex items-start justify-between gap-4 border-b border-rule pb-3">
          <div>
            <D.Title className="text-lg font-semibold">{title}</D.Title>
            {description ? (
              <D.Description className="mt-1 text-base text-ink-muted">{description}</D.Description>
            ) : (
              <D.Description className="sr-only">{title}</D.Description>
            )}
          </div>
          <D.Close
            className="rounded-sm p-1 text-ink-muted hover:bg-paper-sunk hover:text-ink"
            aria-label={closeLabel}
          >
            <X className="size-4" aria-hidden />
          </D.Close>
        </div>
        {children}
      </D.Content>
    </D.Portal>
  );
}
