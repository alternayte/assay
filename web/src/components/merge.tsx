import { useMutation, useQuery } from "@tanstack/react-query";
import { GitMerge, Unlink } from "lucide-react";
import { type FormEvent, useState } from "react";
import { ApiError, api, type Schemas, unwrap } from "@/api/client";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent } from "@/components/ui/dialog";
import { Field, Input } from "@/components/ui/field";
import { Notice } from "@/components/ui/notice";
import { type CopyKey, t } from "@/copy";
import { atLeast, useMe } from "@/lib/session";

type Entity = Schemas["Entity"];

const errorText = (e: unknown) => (e instanceof ApiError ? e.message : t("errors.network"));

/** A search picker for one visible entity. */
export function EntityPicker({
  label,
  hint,
  exclude,
  value,
  onChange,
}: {
  label: string;
  hint?: string;
  exclude?: string;
  value?: Entity;
  onChange: (e?: Entity) => void;
}) {
  const [q, setQ] = useState("");
  const found = useQuery({
    queryKey: ["entities", "pick", q],
    enabled: q.trim().length >= 2 && !value,
    queryFn: async () =>
      unwrap(await api.GET("/api/v1/entities", { params: { query: { q: q.trim(), limit: 8 } } })).entities,
  });
  if (value) {
    return (
      <div className="flex flex-col gap-1">
        <span className="label">{label}</span>
        <div className="flex flex-wrap items-center justify-between gap-2 rounded-sm border border-accent bg-accent-wash px-3 py-1.5">
          <span>{t("merge.picked", { name: value.name })}</span>
          <Button size="sm" variant="ghost" onClick={() => onChange(undefined)}>
            {t("merge.change")}
          </Button>
        </div>
      </div>
    );
  }
  const rows = (found.data ?? []).filter((e) => e.key !== exclude);
  return (
    <div className="flex flex-col gap-2">
      <Field label={label} hint={hint ?? t("merge.searchHint")}>
        {(p) => <Input {...p} type="search" value={q} onChange={(e) => setQ(e.target.value)} />}
      </Field>
      {q.trim().length >= 2 && found.isSuccess && rows.length === 0 && (
        <p className="text-sm text-ink-muted">{t("merge.none")}</p>
      )}
      {rows.length > 0 && (
        <ul className="flex flex-col divide-y divide-rule rounded-sm border border-rule">
          {rows.map((e) => (
            <li key={e.key}>
              <button
                type="button"
                onClick={() => onChange(e)}
                aria-label={t("merge.pick", { name: e.name })}
                className="flex w-full flex-wrap items-baseline justify-between gap-2 px-3 py-1.5 text-left hover:bg-paper-sunk"
              >
                <span>{e.name}</span>
                <span className="font-mono text-xs text-ink-faint">
                  {t(`entityTypes.${e.type}` as CopyKey)} · {e.key}
                </span>
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

/** Merges entities. A reviewer merges at once; a member proposes the merge. */
export function MergeDialog({
  title,
  keys,
  picker,
  onClose,
  onDone,
}: {
  title: string;
  keys: string[];
  picker?: { exclude: string };
  onClose: () => void;
  onDone: (message: string) => void;
}) {
  const reviewer = atLeast(useMe(), "reviewer");
  const [canonical, setCanonical] = useState("");
  const [other, setOther] = useState<Entity>();
  const all = other ? [...keys, other.key] : keys;
  const merge = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.POST("/api/v1/merges", {
          body: { keys: all, canonicalName: reviewer ? canonical.trim() || undefined : undefined },
        }),
      ),
    onSuccess: () => onDone(reviewer ? t("merge.merged") : t("merge.proposed")),
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    merge.mutate();
  };
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent
        title={title}
        description={reviewer ? t("merge.reviewerBody") : t("merge.memberBody")}
        closeLabel={t("merge.close")}
      >
        <form onSubmit={submit} className="flex flex-col gap-4">
          {merge.isError && <Notice tone="danger">{errorText(merge.error)}</Notice>}
          {picker && (
            <EntityPicker
              label={t("merge.search")}
              exclude={picker.exclude}
              value={other}
              onChange={setOther}
            />
          )}
          {reviewer && (
            <Field label={t("merge.canonical")} hint={t("merge.canonicalHint")}>
              {(p) => (
                <Input
                  {...p}
                  maxLength={200}
                  value={canonical}
                  onChange={(e) => setCanonical(e.target.value)}
                />
              )}
            </Field>
          )}
          <div className="flex gap-3">
            <Button onClick={onClose}>{t("merge.cancel")}</Button>
            <Button type="submit" variant="primary" disabled={merge.isPending || all.length < 2}>
              {reviewer ? t("merge.merge") : t("merge.propose")}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

/** Stores that an entity is not the same as another one. Reviewers only. */
export function NotSameDialog({
  entity,
  onClose,
  onDone,
}: {
  entity: { key: string; name: string };
  onClose: () => void;
  onDone: (message: string) => void;
}) {
  const [other, setOther] = useState<Entity>();
  const mark = useMutation({
    mutationFn: async () =>
      unwrap(await api.POST("/api/v1/merges/not-same", { body: { keys: [entity.key, other?.key ?? ""] } })),
    onSuccess: () => onDone(t("merge.notSameDone")),
  });
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent
        title={t("merge.notSameTitle", { name: entity.name })}
        description={t("merge.notSameBody")}
        closeLabel={t("merge.close")}
      >
        <form
          onSubmit={(e) => {
            e.preventDefault();
            mark.mutate();
          }}
          className="flex flex-col gap-4"
        >
          {mark.isError && <Notice tone="danger">{errorText(mark.error)}</Notice>}
          <EntityPicker label={t("merge.search")} exclude={entity.key} value={other} onChange={setOther} />
          <div className="flex gap-3">
            <Button onClick={onClose}>{t("merge.cancel")}</Button>
            <Button type="submit" variant="primary" disabled={!other || mark.isPending}>
              {t("merge.notSameAction")}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

/** Merge with…, Not the same and the active merges with Split, for an entity page. */
export function MergeActions({
  entity,
  onChange,
}: {
  entity: { key: string; name: string };
  onChange: () => void;
}) {
  const reviewer = atLeast(useMe(), "reviewer");
  const [open, setOpen] = useState<"merge" | "different">();
  const [msg, setMsg] = useState<string>();
  const done = (m: string) => {
    setOpen(undefined);
    setMsg(m);
    onChange();
  };
  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap gap-2">
        <Button size="sm" onClick={() => setOpen("merge")}>
          <GitMerge aria-hidden />
          {t("merge.open")}
        </Button>
        {reviewer && (
          <Button size="sm" variant="ghost" onClick={() => setOpen("different")}>
            <Unlink aria-hidden />
            {t("merge.notSame")}
          </Button>
        )}
      </div>
      {msg && <Notice tone="success">{msg}</Notice>}
      {open === "merge" && (
        <MergeDialog
          title={t("merge.title", { name: entity.name })}
          keys={[entity.key]}
          picker={{ exclude: entity.key }}
          onClose={() => setOpen(undefined)}
          onDone={done}
        />
      )}
      {open === "different" && (
        <NotSameDialog entity={entity} onClose={() => setOpen(undefined)} onDone={done} />
      )}
    </div>
  );
}

/** The active merges into an entity, with Split for reviewers. */
export function Merges({ merges, onSplit }: { merges: Schemas["MergeView"][]; onSplit: () => void }) {
  const reviewer = atLeast(useMe(), "reviewer");
  const split = useMutation({
    mutationFn: async (id: number) =>
      unwrap(await api.POST("/api/v1/merges/{id}/split", { params: { path: { id } } })),
    onSuccess: onSplit,
  });
  if (merges.length === 0) return null;
  return (
    <div className="flex flex-col gap-2 rounded-sm border border-rule px-3 py-2">
      <span className="label">{t("merge.merges")}</span>
      <p className="text-sm text-ink-muted">{t("merge.mergesBody")}</p>
      {split.isError && <Notice tone="danger">{errorText(split.error)}</Notice>}
      <ul className="flex flex-col divide-y divide-rule">
        {merges.map((m) => (
          <li key={m.id} className="flex flex-wrap items-center justify-between gap-2 py-1.5">
            <span>
              {m.losers.map((l) => l.name).join(", ")}
              {m.auto && <span className="ml-2 font-mono text-xs text-ink-faint">{t("merge.auto")}</span>}
            </span>
            {reviewer && (
              <Button
                size="sm"
                variant="danger"
                disabled={split.isPending}
                onClick={() => split.mutate(m.id)}
              >
                {t("merge.split")}
              </Button>
            )}
          </li>
        ))}
      </ul>
    </div>
  );
}
