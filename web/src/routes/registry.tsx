import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import { Pencil, Plus, RotateCcw } from "lucide-react";
import { type FormEvent, useState } from "react";
import { ApiError, api, type Schemas, unwrap } from "@/api/client";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent } from "@/components/ui/dialog";
import { Field, Input, Select, Textarea } from "@/components/ui/field";
import { Notice } from "@/components/ui/notice";
import { Empty, Sheet } from "@/components/ui/sheet";
import { SkeletonRows } from "@/components/ui/skeleton";
import { type CopyKey, t } from "@/copy";
import { cn } from "@/lib/cn";
import { formatAgo, optionLabel } from "@/lib/format";

type ThingRecord = Schemas["ThingRecord"];

function messageOf(e: unknown): string {
  return e instanceof ApiError ? e.message : t("errors.network");
}

/** The versions of a registry record, a diff of each and revert. */
export function History({ recordKey }: { recordKey: string }) {
  const qc = useQueryClient();
  const record = useQuery({
    queryKey: ["record", recordKey],
    queryFn: async () =>
      unwrap(await api.GET("/api/v1/registry/records/{key}", { params: { path: { key: recordKey } } })),
  });
  const [selected, setSelected] = useState<number>();
  const versions = record.data?.versions ?? [];
  const current = versions[0]?.id;
  const shown = selected ?? current;
  const version = useQuery({
    queryKey: ["record", recordKey, "version", shown],
    enabled: shown !== undefined,
    queryFn: async () =>
      unwrap(
        await api.GET("/api/v1/registry/records/{key}/versions/{id}", {
          params: { path: { key: recordKey, id: shown ?? 0 } },
        }),
      ),
  });
  const revert = useMutation({
    mutationFn: async (id: number) =>
      unwrap(
        await api.POST("/api/v1/registry/records/{key}/revert", {
          params: { path: { key: recordKey } },
          body: { versionId: id },
        }),
      ),
    onSuccess: () => {
      setSelected(undefined);
      qc.invalidateQueries({ queryKey: ["record"] });
      qc.invalidateQueries({ queryKey: ["terms"] });
      qc.invalidateQueries({ queryKey: ["term"] });
    },
  });
  return (
    <Sheet title={t("registry.history")} description={t("registry.historyBody")}>
      {record.isPending ? (
        <SkeletonRows label={t("app.loading")} rows={3} />
      ) : record.isError ? (
        <Notice tone="danger">{t("app.error")}</Notice>
      ) : (
        <div className="grid gap-4 md:grid-cols-[minmax(0,14rem)_1fr]">
          <ol className="flex flex-col gap-1" aria-label={t("registry.versions")}>
            {versions.map((v, i) => (
              <li key={v.id}>
                <button
                  type="button"
                  onClick={() => setSelected(v.id)}
                  aria-current={v.id === shown || undefined}
                  className={cn(
                    "flex w-full flex-col items-start rounded-sm border px-2 py-1 text-left transition-ink",
                    v.id === shown
                      ? "border-accent bg-accent-wash"
                      : "border-transparent hover:bg-paper-sunk",
                  )}
                >
                  <span className="font-mono text-xs">
                    {t("registry.version", { n: versions.length - i })}
                    {v.id === current ? ` · ${t("registry.current")}` : ""}
                  </span>
                  <span className="text-sm">{v.editedBy || t("registry.unknownEditor")}</span>
                  <span className="text-xs text-ink-faint">{formatAgo(v.at)}</span>
                </button>
              </li>
            ))}
          </ol>
          <div className="flex flex-col gap-3">
            {revert.isError && <Notice tone="danger">{messageOf(revert.error)}</Notice>}
            {version.isPending ? (
              <SkeletonRows label={t("app.loading")} rows={4} />
            ) : version.isError ? (
              <Notice tone="danger">{t("app.error")}</Notice>
            ) : (
              <ul className="flex flex-col font-mono text-sm" aria-label={t("registry.diff")}>
                {version.data.diff.map((l, i) => (
                  <li
                    // biome-ignore lint/suspicious/noArrayIndexKey: diff lines have no other identity.
                    key={i}
                    data-op={l.op}
                    className={cn(
                      "flex gap-2 border-l-4 px-2 py-0.5",
                      l.op === "+" && "border-supported bg-wash-supported",
                      l.op === "-" && "border-danger bg-wash-danger line-through decoration-danger/60",
                      l.op === "=" && "border-transparent text-ink-muted",
                    )}
                  >
                    <span className="w-16 shrink-0 text-xs tracking-wide uppercase">
                      {l.op === "+" ? t("registry.added") : l.op === "-" ? t("registry.removed") : ""}
                    </span>
                    <span className="break-words">{l.text}</span>
                  </li>
                ))}
              </ul>
            )}
            {shown !== undefined && shown !== current && (
              <div>
                <Button onClick={() => revert.mutate(shown)} disabled={revert.isPending}>
                  <RotateCcw aria-hidden />
                  {t("registry.revert")}
                </Button>
              </div>
            )}
          </div>
        </div>
      )}
    </Sheet>
  );
}

const thingTypes = [
  "service",
  "api",
  "system",
  "resource",
  "repository",
  "project",
  "initiative",
  "issue",
  "document",
  "decision",
  "environment",
  "team",
  "person",
] as const;

/** Creates a thing record, or saves a new version of one. */
export function ThingDialog({ recordKey, record }: { recordKey?: string; record?: ThingRecord }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const owners = useQuery({
    queryKey: ["owners"],
    queryFn: async () => {
      const [teams, people] = await Promise.all([
        api.GET("/api/v1/entities", { params: { query: { type: "team", limit: 500 } } }),
        api.GET("/api/v1/entities", { params: { query: { type: "person", limit: 500 } } }),
      ]);
      return [...unwrap(teams).entities, ...unwrap(people).entities];
    },
  });
  const [open, setOpen] = useState(false);
  const [form, setForm] = useState<ThingRecord>(record ?? { type: "service", name: "" });
  const [aliases, setAliases] = useState((record?.aliases ?? []).join(", "));
  const [links, setLinks] = useState((record?.links ?? []).join("\n"));
  const [error, setError] = useState<string>();
  const save = useMutation({
    mutationFn: async () => {
      const thing: ThingRecord = {
        ...form,
        aliases: aliases
          .split(",")
          .map((s) => s.trim())
          .filter(Boolean),
        links: links
          .split("\n")
          .map((s) => s.trim())
          .filter(Boolean),
      };
      const body = { kind: "thing" as const, thing };
      return recordKey
        ? unwrap(
            await api.PUT("/api/v1/registry/records/{key}", { params: { path: { key: recordKey } }, body }),
          )
        : unwrap(await api.POST("/api/v1/registry/records", { body }));
    },
    onSuccess: (r) => {
      setOpen(false);
      qc.invalidateQueries({ queryKey: ["record"] });
      qc.invalidateQueries({ queryKey: ["entities"] });
      if (!recordKey) navigate({ to: "/registry/$key", params: { key: r.key } });
    },
    onError: (e) => setError(messageOf(e)),
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    setError(undefined);
    save.mutate();
  };
  const title = recordKey ? t("registry.edit") : t("registry.create");
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <Button variant={recordKey ? "secondary" : "primary"} onClick={() => setOpen(true)}>
        {recordKey ? <Pencil aria-hidden /> : <Plus aria-hidden />}
        {title}
      </Button>
      <DialogContent title={title} description={t("registry.formBody")} closeLabel={t("registry.close")}>
        <form onSubmit={submit} className="flex flex-col gap-4">
          {error && <Notice tone="danger">{error}</Notice>}
          <Field label={t("registry.type")}>
            {(p) => (
              <Select
                {...p}
                value={form.type}
                disabled={!!recordKey}
                onChange={(e) => setForm({ ...form, type: e.target.value })}
              >
                {thingTypes.map((x) => (
                  <option key={x} value={x}>
                    {t(`entityTypes.${x}` as CopyKey)}
                  </option>
                ))}
              </Select>
            )}
          </Field>
          <Field label={t("registry.name")}>
            {(p) => (
              <Input
                {...p}
                required
                maxLength={200}
                value={form.name}
                onChange={(e) => setForm({ ...form, name: e.target.value })}
              />
            )}
          </Field>
          <Field label={t("registry.aliases")} hint={t("registry.aliasesHint")}>
            {(p) => <Input {...p} value={aliases} onChange={(e) => setAliases(e.target.value)} />}
          </Field>
          <Field label={t("registry.description")}>
            {(p) => (
              <Textarea
                {...p}
                rows={3}
                maxLength={4000}
                value={form.description ?? ""}
                onChange={(e) => setForm({ ...form, description: e.target.value })}
              />
            )}
          </Field>
          <Field label={t("registry.owner")}>
            {(p) => (
              <Select
                {...p}
                value={form.owner ?? ""}
                onChange={(e) => setForm({ ...form, owner: e.target.value || undefined })}
              >
                <option value="">{t("registry.noOwner")}</option>
                {owners.data?.map((o) => (
                  <option key={o.key} value={o.key}>
                    {optionLabel(owners.data ?? [], o)}
                  </option>
                ))}
              </Select>
            )}
          </Field>
          <Field label={t("registry.links")} hint={t("registry.linksHint")}>
            {(p) => <Textarea {...p} rows={2} value={links} onChange={(e) => setLinks(e.target.value)} />}
          </Field>
          <div>
            <Button type="submit" variant="primary" disabled={save.isPending}>
              {t("registry.save")}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

/** A registry record of a thing: its fields and its history. Terms open on their Glossary page. */
export function RecordPage() {
  const { key } = useParams({ from: "/authed/registry/$key" });
  const record = useQuery({
    queryKey: ["record", key],
    queryFn: async () =>
      unwrap(await api.GET("/api/v1/registry/records/{key}", { params: { path: { key } } })),
  });
  if (record.isPending) return <SkeletonRows label={t("app.loading")} rows={8} />;
  if (record.isError) return <Notice tone="danger">{t("registry.notFound")}</Notice>;
  const r = record.data;
  if (r.kind === "term") {
    return (
      <Empty
        action={
          <Link to="/glossary/$key" params={{ key }} className="text-accent hover:underline">
            {t("registry.openTerm")}
          </Link>
        }
      >
        {t("registry.isTerm")}
      </Empty>
    );
  }
  const x = r.thing as ThingRecord;
  return (
    <div className="flex flex-col gap-6">
      <header className="flex flex-wrap items-end justify-between gap-4">
        <div className="flex flex-col gap-2">
          <span className="text-xs font-medium tracking-wide text-ink-muted uppercase">
            {t("registry.title")}
          </span>
          <h1 className="text-3xl font-semibold tracking-tight">{x.name}</h1>
          <p className="font-mono text-xs break-all text-ink-faint">{key}</p>
        </div>
        <ThingDialog recordKey={key} record={x} />
      </header>
      <Sheet title={t("registry.fields")}>
        <dl className="grid gap-x-6 gap-y-3 sm:grid-cols-[max-content_1fr]">
          <dt className="text-xs font-medium tracking-wide text-ink-muted uppercase">{t("registry.type")}</dt>
          <dd>{t(`entityTypes.${x.type}` as CopyKey)}</dd>
          <dt className="text-xs font-medium tracking-wide text-ink-muted uppercase">
            {t("registry.aliases")}
          </dt>
          <dd>{(x.aliases ?? []).join(", ") || "—"}</dd>
          <dt className="text-xs font-medium tracking-wide text-ink-muted uppercase">
            {t("registry.description")}
          </dt>
          <dd>{x.description || "—"}</dd>
          <dt className="text-xs font-medium tracking-wide text-ink-muted uppercase">
            {t("registry.owner")}
          </dt>
          <dd className="font-mono text-sm break-all">{x.owner || "—"}</dd>
          <dt className="text-xs font-medium tracking-wide text-ink-muted uppercase">
            {t("registry.relations")}
          </dt>
          <dd>
            {(x.relations ?? []).length === 0 ? (
              "—"
            ) : (
              <ul className="flex flex-col gap-1">
                {(x.relations ?? []).map((rel) => (
                  <li key={`${rel.predicate}-${rel.object}`} className="text-sm">
                    <span className="text-ink-muted">{rel.predicate.replaceAll("_", " ")}</span>{" "}
                    <span className="font-mono break-all">{rel.object}</span>
                  </li>
                ))}
              </ul>
            )}
          </dd>
          <dt className="text-xs font-medium tracking-wide text-ink-muted uppercase">
            {t("registry.links")}
          </dt>
          <dd>
            {(x.links ?? []).length === 0 ? (
              "—"
            ) : (
              <ul className="flex flex-col gap-1">
                {(x.links ?? []).map((l) => (
                  <li key={l}>
                    <a
                      href={l}
                      target="_blank"
                      rel="noopener noreferrer"
                      className="break-all text-accent hover:underline"
                    >
                      {l}
                    </a>
                  </li>
                ))}
              </ul>
            )}
          </dd>
        </dl>
      </Sheet>
      <History recordKey={key} />
    </div>
  );
}
