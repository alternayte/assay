import { useNavigate } from "@tanstack/react-router";
import { Command } from "cmdk";
import { Dialog as D } from "radix-ui";
import { useEffect } from "react";
import { t } from "@/copy";
import { navItems } from "@/lib/nav";
import { atLeast, useMe } from "@/lib/session";

export function Palette({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  const me = useMe();
  const navigate = useNavigate();

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key.toLowerCase() === "k" && (e.metaKey || e.ctrlKey)) {
        e.preventDefault();
        onOpenChange(!open);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open, onOpenChange]);

  const items = navItems.filter((i) => atLeast(me, i.role));
  const groups = [
    { key: "pages", heading: t("palette.groupPages") },
    { key: "settings", heading: t("palette.groupSettings") },
  ] as const;

  const go = (to: string) => {
    onOpenChange(false);
    navigate({ to });
  };

  return (
    <D.Root open={open} onOpenChange={onOpenChange}>
      <D.Portal>
        <D.Overlay className="fixed inset-0 z-40 bg-ink/30 dark:bg-paper-sunk/70" />
        <D.Content className="fixed top-[12vh] left-1/2 z-50 w-[calc(100vw-2rem)] max-w-xl -translate-x-1/2 overflow-hidden rounded-md border border-rule-strong bg-paper-raised shadow-sheet">
          <D.Title className="sr-only">{t("palette.title")}</D.Title>
          <D.Description className="sr-only">{t("palette.placeholder")}</D.Description>
          <Command label={t("palette.title")} className="flex flex-col">
            <Command.Input
              placeholder={t("palette.placeholder")}
              className="h-12 border-b border-rule bg-transparent px-4 text-base text-ink outline-none placeholder:text-ink-faint"
            />
            <Command.List className="max-h-80 overflow-y-auto p-2">
              <Command.Empty className="px-2 py-3 text-base text-ink-muted">
                {t("palette.empty")}
              </Command.Empty>
              {groups.map((g) => {
                const inGroup = items.filter((i) => i.group === g.key);
                if (inGroup.length === 0) return null;
                return (
                  <Command.Group
                    key={g.key}
                    heading={g.heading}
                    className="[&_[cmdk-group-heading]]:label [&_[cmdk-group-heading]]:px-2 [&_[cmdk-group-heading]]:py-1"
                  >
                    {inGroup.map((i) => (
                      <Command.Item
                        key={`${g.key}-${i.to}`}
                        value={`${g.heading} ${t(i.label)}`}
                        onSelect={() => go(i.to)}
                        className="flex cursor-pointer items-center justify-between rounded-sm px-2 py-1.5 text-base text-ink data-[selected=true]:bg-accent-wash"
                      >
                        <span>{t(i.label)}</span>
                        <span className="font-mono text-xs text-ink-faint">{i.to}</span>
                      </Command.Item>
                    ))}
                  </Command.Group>
                );
              })}
            </Command.List>
          </Command>
        </D.Content>
      </D.Portal>
    </D.Root>
  );
}
