import { useQuery } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { Command } from "cmdk";
import { ArrowUpRight } from "lucide-react";
import { Dialog as D } from "radix-ui";
import { useEffect, useState } from "react";
import { api, unwrap } from "@/api/client";
import { type CopyKey, t } from "@/copy";
import { navItems } from "@/lib/nav";
import { atLeast, useMe } from "@/lib/session";

const itemClass =
  "flex cursor-pointer items-center justify-between gap-3 rounded-sm px-2 py-1.5 text-base text-ink data-[selected=true]:bg-accent-wash";
const groupClass =
  "[&_[cmdk-group-heading]]:label [&_[cmdk-group-heading]]:px-2 [&_[cmdk-group-heading]]:py-1";

/** The query after the user stops typing for a moment. */
function useSettled(value: string, ms = 200) {
  const [settled, setSettled] = useState(value);
  useEffect(() => {
    const id = setTimeout(() => setSettled(value), ms);
    return () => clearTimeout(id);
  }, [value, ms]);
  return settled;
}

/** Finds entities, terms, source items, questions, pages and settings. */
export function Palette({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  const me = useMe();
  const navigate = useNavigate();
  const [query, setQuery] = useState("");
  const q = useSettled(query.trim());

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
  useEffect(() => {
    if (!open) setQuery("");
  }, [open]);

  const search = useQuery({
    queryKey: ["palette", q],
    enabled: open && q.length >= 2,
    queryFn: async () => unwrap(await api.GET("/api/v1/search", { params: { query: { q, limit: 8 } } })),
  });
  const questions = useQuery({
    queryKey: ["questions"],
    enabled: open,
    queryFn: async () => unwrap(await api.GET("/api/v1/questions")).questions,
  });

  const needle = query.trim().toLowerCase();
  const matches = (s: string) => needle === "" || s.toLowerCase().includes(needle);
  const items = navItems.filter((i) => atLeast(me, i.role) && matches(t(i.label)));
  const groups = [
    { key: "pages", heading: t("palette.groupPages") },
    { key: "settings", heading: t("palette.groupSettings") },
  ] as const;
  const found = q.length >= 2 && q === query.trim() ? search.data : undefined;
  const asked = needle.length >= 2 ? (questions.data ?? []).filter((x) => matches(x.text)).slice(0, 5) : [];

  const go = (to: string, params?: Record<string, string>) => {
    onOpenChange(false);
    navigate({ to, params });
  };

  return (
    <D.Root open={open} onOpenChange={onOpenChange}>
      <D.Portal>
        <D.Overlay className="fixed inset-0 z-40 bg-ink/30 dark:bg-paper-sunk/70" />
        <D.Content className="fixed top-[12vh] left-1/2 z-50 w-[calc(100vw-2rem)] max-w-xl -translate-x-1/2 overflow-hidden rounded-md border border-rule-strong bg-paper-raised shadow-sheet">
          <D.Title className="sr-only">{t("palette.title")}</D.Title>
          <D.Description className="sr-only">{t("palette.placeholder")}</D.Description>
          <Command label={t("palette.title")} className="flex flex-col" shouldFilter={false}>
            <Command.Input
              value={query}
              onValueChange={setQuery}
              placeholder={t("palette.placeholder")}
              className="h-12 border-b border-rule bg-transparent px-4 text-base text-ink outline-none placeholder:text-ink-faint"
            />
            <Command.List className="max-h-96 overflow-y-auto p-2">
              {search.isFetching && (
                <Command.Loading className="px-2 py-1 text-sm text-ink-muted">
                  {t("palette.searching")}
                </Command.Loading>
              )}
              <Command.Empty className="px-2 py-3 text-base text-ink-muted">
                {t("palette.empty")}
              </Command.Empty>
              {found && found.entities.length > 0 && (
                <Command.Group heading={t("palette.groupEntities")} className={groupClass}>
                  {found.entities.map((e) => (
                    <Command.Item
                      key={e.key}
                      value={`entity ${e.key}`}
                      onSelect={() => go("/e/$key", { key: e.key })}
                      className={itemClass}
                    >
                      <span className="break-words">{e.name}</span>
                      <span className="font-mono text-xs text-ink-faint">
                        {t(`entityTypes.${e.type}` as CopyKey)}
                      </span>
                    </Command.Item>
                  ))}
                </Command.Group>
              )}
              {found && found.results.length > 0 && (
                <Command.Group heading={t("palette.groupItems")} className={groupClass}>
                  {found.results.map((r) => (
                    <Command.Item
                      key={r.itemId}
                      value={`item ${r.itemId}`}
                      onSelect={() => {
                        onOpenChange(false);
                        window.open(r.url, "_blank", "noopener,noreferrer");
                      }}
                      className={itemClass}
                    >
                      <span className="break-words">{r.title}</span>
                      <span className="flex shrink-0 items-center gap-1 font-mono text-xs text-ink-faint">
                        {r.connectionName}
                        <ArrowUpRight className="size-3.5" aria-hidden />
                      </span>
                    </Command.Item>
                  ))}
                </Command.Group>
              )}
              {asked.length > 0 && (
                <Command.Group heading={t("palette.groupQuestions")} className={groupClass}>
                  {asked.map((x) => (
                    <Command.Item
                      key={x.id}
                      value={`question ${x.id}`}
                      onSelect={() => go("/questions/$id", { id: String(x.id) })}
                      className={itemClass}
                    >
                      <span className="break-words">{x.text}</span>
                    </Command.Item>
                  ))}
                </Command.Group>
              )}
              {groups.map((g) => {
                const inGroup = items.filter((i) => i.group === g.key);
                if (inGroup.length === 0) return null;
                return (
                  <Command.Group key={g.key} heading={g.heading} className={groupClass}>
                    {inGroup.map((i) => (
                      <Command.Item
                        key={`${g.key}-${i.to}`}
                        value={`${g.key} ${i.to}`}
                        onSelect={() => go(i.to)}
                        className={itemClass}
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
