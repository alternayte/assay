import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import { Download, Pencil, Plus, Upload } from "lucide-react";
import { type FormEvent, useRef, useState } from "react";
import { ApiError, api, type Schemas, unwrap } from "@/api/client";
import { TermStatus } from "@/components/terms";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent } from "@/components/ui/dialog";
import { Field, Input, Select, Textarea } from "@/components/ui/field";
import { Notice } from "@/components/ui/notice";
import { Empty, Sheet } from "@/components/ui/sheet";
import { SkeletonRows } from "@/components/ui/skeleton";
import { Table, Td, Th } from "@/components/ui/table";
import { t } from "@/copy";
import { optionLabel } from "@/lib/format";
import { atLeast, useMe } from "@/lib/session";
import { History } from "@/routes/registry";

type TermRecord = Schemas["TermRecord"];
type ImportResult = Schemas["ImportResult"];

function messageOf(e: unknown): string {
  return e instanceof ApiError ? e.message : t("errors.network");
}

function useTerms() {
  return useQuery({
    queryKey: ["terms"],
    queryFn: async () => unwrap(await api.GET("/api/v1/terms")).terms,
  });
}

function useDomains() {
  return useQuery({
    queryKey: ["domains"],
    queryFn: async () => unwrap(await api.GET("/api/v1/domains")).domains,
  });
}

/** The Glossary: the visible terms with search, filters, create and CSV. */
export function GlossaryPage() {
  const terms = useTerms();
  const domains = useDomains();
  const [q, setQ] = useState("");
  const [status, setStatus] = useState("");
  const [domain, setDomain] = useState("");
  const [owner, setOwner] = useState("");
  const needle = q.trim().toLowerCase();
  const rows = (terms.data ?? []).filter(
    (x) =>
      (!status || x.status === status) &&
      (!domain || String(x.domainId ?? "") === domain) &&
      (!owner || (x.owner ?? "").toLowerCase().includes(owner.trim().toLowerCase())) &&
      (!needle ||
        [x.name, ...x.synonyms, ...x.deprecatedForms, x.definition].some((s) =>
          s.toLowerCase().includes(needle),
        )),
  );
  return (
    <div className="flex flex-col gap-6">
      <header className="flex flex-wrap items-end justify-between gap-4">
        <div className="flex flex-col gap-1">
          <h1 className="text-3xl font-semibold tracking-tight">{t("glossary.title")}</h1>
          <p className="text-base text-ink-muted">{t("glossary.body")}</p>
        </div>
        <div className="flex flex-wrap gap-2">
          <ExportButton />
          <ImportButton />
          <TermDialog />
        </div>
      </header>
      <div className="grid gap-3 sm:grid-cols-[minmax(0,2fr)_repeat(3,minmax(0,1fr))]">
        <Field label={t("glossary.search")}>
          {(p) => <Input {...p} type="search" value={q} onChange={(e) => setQ(e.target.value)} />}
        </Field>
        <Field label={t("glossary.statusLabel")}>
          {(p) => (
            <Select {...p} value={status} onChange={(e) => setStatus(e.target.value)}>
              <option value="">{t("glossary.all")}</option>
              <option value="approved">{t("glossary.status.approved")}</option>
              <option value="draft">{t("glossary.status.draft")}</option>
              <option value="deprecated">{t("glossary.status.deprecated")}</option>
            </Select>
          )}
        </Field>
        <Field label={t("glossary.domain")}>
          {(p) => (
            <Select {...p} value={domain} onChange={(e) => setDomain(e.target.value)}>
              <option value="">{t("glossary.all")}</option>
              {domains.data?.map((d) => (
                <option key={d.id} value={d.id}>
                  {d.name}
                </option>
              ))}
            </Select>
          )}
        </Field>
        <Field label={t("glossary.owner")}>
          {(p) => <Input {...p} type="search" value={owner} onChange={(e) => setOwner(e.target.value)} />}
        </Field>
      </div>
      {terms.isPending ? (
        <SkeletonRows label={t("app.loading")} rows={6} />
      ) : terms.isError ? (
        <Notice tone="danger">{t("app.error")}</Notice>
      ) : rows.length === 0 ? (
        <Empty action={<TermDialog />}>{t("glossary.empty")}</Empty>
      ) : (
        <Table>
          <thead>
            <tr>
              <Th>{t("glossary.colTerm")}</Th>
              <Th>{t("glossary.statusLabel")}</Th>
              <Th className="hidden sm:table-cell">{t("glossary.domain")}</Th>
              <Th className="hidden md:table-cell">{t("glossary.owner")}</Th>
              <Th className="hidden lg:table-cell">{t("glossary.definition")}</Th>
            </tr>
          </thead>
          <tbody>
            {rows.map((x) => (
              <tr key={x.key} data-testid="term-row">
                <Td>
                  <Link
                    to="/glossary/$key"
                    params={{ key: x.key }}
                    className="font-medium text-accent underline-offset-2 hover:underline"
                  >
                    {x.name}
                  </Link>
                  {x.synonyms.length > 0 && (
                    <div className="text-xs text-ink-faint">{x.synonyms.join(", ")}</div>
                  )}
                </Td>
                <Td>
                  {x.status ? (
                    <TermStatus status={x.status} />
                  ) : (
                    <span className="text-ink-faint">{t("glossary.noStatus")}</span>
                  )}
                </Td>
                <Td className="hidden sm:table-cell">{x.domain || t("glossary.noDomain")}</Td>
                <Td className="hidden md:table-cell">{x.owner}</Td>
                <Td className="hidden max-w-md truncate text-ink-muted lg:table-cell">{x.definition}</Td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}
    </div>
  );
}

function ExportButton() {
  const [error, setError] = useState<string>();
  const download = async () => {
    setError(undefined);
    try {
      const csv = unwrap(await api.GET("/api/v1/registry/terms.csv", { parseAs: "text" })) as string;
      const url = URL.createObjectURL(new Blob([csv], { type: "text/csv" }));
      const a = document.createElement("a");
      a.href = url;
      a.download = "glossary.csv";
      a.click();
      URL.revokeObjectURL(url);
    } catch (e) {
      setError(messageOf(e));
    }
  };
  return (
    <>
      <Button onClick={download}>
        <Download aria-hidden />
        {t("glossary.export")}
      </Button>
      {error && <Notice tone="danger">{error}</Notice>}
    </>
  );
}

function ImportButton() {
  const qc = useQueryClient();
  const input = useRef<HTMLInputElement>(null);
  const [open, setOpen] = useState(false);
  const run = useMutation({
    mutationFn: async (file: File) =>
      unwrap(
        await api.POST("/api/v1/registry/terms.csv", {
          body: await file.text(),
          bodySerializer: (b: string) => b,
          headers: { "Content-Type": "text/csv" },
        }),
      ),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["terms"] }),
    onSettled: () => setOpen(true),
  });
  return (
    <>
      <input
        ref={input}
        type="file"
        accept=".csv,text/csv"
        className="sr-only"
        aria-label={t("glossary.importFile")}
        data-testid="import-file"
        onChange={(e) => {
          const f = e.target.files?.[0];
          if (f) run.mutate(f);
          e.target.value = "";
        }}
      />
      <Button onClick={() => input.current?.click()} disabled={run.isPending}>
        <Upload aria-hidden />
        {t("glossary.import")}
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent title={t("glossary.importTitle")} closeLabel={t("glossary.close")}>
          {run.isError ? (
            <Notice tone="danger">{messageOf(run.error)}</Notice>
          ) : (
            run.data && <ImportSummary result={run.data} />
          )}
        </DialogContent>
      </Dialog>
    </>
  );
}

function ImportSummary({ result }: { result: ImportResult }) {
  return (
    <div className="flex flex-col gap-3">
      <p data-testid="import-summary">
        {t("glossary.importDone", {
          created: result.created,
          updated: result.updated,
          unchanged: result.unchanged,
          proposals: result.proposals,
        })}
      </p>
      {result.errors.length > 0 && (
        <Notice tone="danger">
          <ul className="flex flex-col gap-1">
            {result.errors.map((e) => (
              <li key={e.line}>{t("glossary.importError", { line: e.line, message: e.message })}</li>
            ))}
          </ul>
        </Notice>
      )}
    </div>
  );
}

/** Creates a term, or saves a new version of a registry term. */
export function TermDialog({ recordKey, record }: { recordKey?: string; record?: TermRecord }) {
  const me = useMe();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const domains = useDomains();
  const terms = useTerms();
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
  const [form, setForm] = useState<TermRecord>(record ?? { name: "" });
  const [synonyms, setSynonyms] = useState((record?.synonyms ?? []).join(", "));
  const [error, setError] = useState<string>();
  const [proposal, setProposal] = useState(false);
  const reviewer = atLeast(me, "reviewer");
  const save = useMutation({
    mutationFn: async () => {
      const term: TermRecord = {
        ...form,
        synonyms: synonyms
          .split(",")
          .map((s) => s.trim())
          .filter(Boolean),
      };
      const body = { kind: "term" as const, term };
      return recordKey
        ? unwrap(
            await api.PUT("/api/v1/registry/records/{key}", { params: { path: { key: recordKey } }, body }),
          )
        : unwrap(await api.POST("/api/v1/registry/records", { body }));
    },
    onSuccess: (r) => {
      qc.invalidateQueries({ queryKey: ["terms"] });
      qc.invalidateQueries({ queryKey: ["record"] });
      qc.invalidateQueries({ queryKey: ["term"] });
      if (r.proposal) {
        setProposal(true);
        return;
      }
      setOpen(false);
      if (!recordKey) navigate({ to: "/glossary/$key", params: { key: r.key } });
    },
    onError: (e) => setError(messageOf(e)),
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    setError(undefined);
    save.mutate();
  };
  const title = recordKey ? t("glossary.edit") : t("glossary.create");
  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        setOpen(o);
        setProposal(false);
      }}
    >
      <Button variant={recordKey ? "secondary" : "primary"} onClick={() => setOpen(true)}>
        {recordKey ? <Pencil aria-hidden /> : <Plus aria-hidden />}
        {title}
      </Button>
      <DialogContent title={title} description={t("glossary.formBody")} closeLabel={t("glossary.close")}>
        {proposal ? (
          <Notice tone="success">{t("glossary.proposed")}</Notice>
        ) : (
          <form onSubmit={submit} className="flex flex-col gap-4">
            {error && <Notice tone="danger">{error}</Notice>}
            <Field label={t("glossary.name")}>
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
            <Field label={t("glossary.synonyms")} hint={t("glossary.synonymsHint")}>
              {(p) => <Input {...p} value={synonyms} onChange={(e) => setSynonyms(e.target.value)} />}
            </Field>
            <Field label={t("glossary.definition")}>
              {(p) => (
                <Textarea
                  {...p}
                  rows={3}
                  maxLength={4000}
                  value={form.definition ?? ""}
                  onChange={(e) => setForm({ ...form, definition: e.target.value })}
                />
              )}
            </Field>
            <Field label={t("glossary.domain")}>
              {(p) => (
                <Select
                  {...p}
                  value={form.domainId ?? ""}
                  onChange={(e) =>
                    setForm({ ...form, domainId: e.target.value ? Number(e.target.value) : undefined })
                  }
                >
                  <option value="">{t("glossary.noDomain")}</option>
                  {domains.data?.map((d) => (
                    <option key={d.id} value={d.id}>
                      {d.name}
                    </option>
                  ))}
                </Select>
              )}
            </Field>
            <Field label={t("glossary.owner")}>
              {(p) => (
                <Select
                  {...p}
                  value={form.owner ?? ""}
                  onChange={(e) => setForm({ ...form, owner: e.target.value || undefined })}
                >
                  <option value="">{t("glossary.noOwner")}</option>
                  {owners.data?.map((o) => (
                    <option key={o.key} value={o.key}>
                      {optionLabel(owners.data ?? [], o)}
                    </option>
                  ))}
                </Select>
              )}
            </Field>
            <Field label={t("glossary.preferred")} hint={t("glossary.preferredHint")}>
              {(p) => (
                <Select
                  {...p}
                  value={form.preferred ?? ""}
                  onChange={(e) => setForm({ ...form, preferred: e.target.value || undefined })}
                >
                  <option value="">{t("glossary.noPreferred")}</option>
                  {terms.data
                    ?.filter((x) => x.key !== recordKey)
                    .map((x) => (
                      <option key={x.key} value={x.key}>
                        {optionLabel(terms.data ?? [], x)}
                      </option>
                    ))}
                </Select>
              )}
            </Field>
            {reviewer && (
              <Field label={t("glossary.statusLabel")} hint={t("glossary.statusHint")}>
                {(p) => (
                  <Select
                    {...p}
                    value={form.status ?? ""}
                    onChange={(e) =>
                      setForm({ ...form, status: (e.target.value || undefined) as TermRecord["status"] })
                    }
                  >
                    <option value="draft">{t("glossary.status.draft")}</option>
                    <option value="approved">{t("glossary.status.approved")}</option>
                    <option value="deprecated">{t("glossary.status.deprecated")}</option>
                  </Select>
                )}
              </Field>
            )}
            <div className="flex gap-3">
              <Button type="submit" variant="primary" disabled={save.isPending}>
                {t("glossary.save")}
              </Button>
            </div>
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
}

type EntityDetail = Schemas["Detail"];

/** A term page: every vocabulary field, usage per connection and history. */
export function TermPage() {
  const { key } = useParams({ from: "/authed/glossary/$key" });
  const term = useQuery({
    queryKey: ["term", key],
    queryFn: async () => unwrap(await api.GET("/api/v1/terms/{key}", { params: { path: { key } } })),
  });
  const registry = key.startsWith("registry:");
  const record = useQuery({
    queryKey: ["record", key],
    enabled: registry,
    queryFn: async () =>
      unwrap(await api.GET("/api/v1/registry/records/{key}", { params: { path: { key } } })),
  });
  if (term.isPending) return <SkeletonRows label={t("app.loading")} rows={8} />;
  if (term.isError) return <Notice tone="danger">{t("glossary.notFound")}</Notice>;
  const x = term.data;
  return (
    <div className="flex flex-col gap-6">
      <header className="flex flex-wrap items-end justify-between gap-4">
        <div className="flex flex-col gap-2">
          <Link to="/glossary" className="text-sm text-accent underline-offset-2 hover:underline">
            {t("glossary.back")}
          </Link>
          <div className="flex flex-wrap items-center gap-3">
            <h1 className="text-3xl font-semibold tracking-tight">{x.name}</h1>
            {x.status ? (
              <TermStatus status={x.status} />
            ) : (
              <span className="text-sm text-ink-faint">{t("glossary.noStatus")}</span>
            )}
          </div>
          <p className="font-mono text-xs break-all text-ink-faint">{x.key}</p>
        </div>
        {registry && record.data?.term ? (
          <TermDialog recordKey={key} record={record.data.term} />
        ) : (
          !registry && <p className="max-w-sm text-sm text-ink-muted">{t("glossary.sourceTerm")}</p>
        )}
      </header>
      <TermFields termKey={key} />
      {registry && <History recordKey={key} />}
    </div>
  );
}

/** The vocabulary fields of a term: definition, fields, usage and sources. */
export function TermFields({ termKey }: { termKey: string }) {
  const key = termKey;
  const term = useQuery({
    queryKey: ["term", key],
    queryFn: async () => unwrap(await api.GET("/api/v1/terms/{key}", { params: { path: { key } } })),
  });
  const entity = useQuery({
    queryKey: ["entity", key],
    queryFn: async () => unwrap(await api.GET("/api/v1/entities/{key}", { params: { path: { key } } })),
  });
  const terms = useTerms();
  if (term.isPending) return <SkeletonRows label={t("app.loading")} rows={6} />;
  if (term.isError) return <Notice tone="danger">{t("glossary.notFound")}</Notice>;
  const x = term.data;
  const nameOf = (k?: string) => terms.data?.find((y) => y.key === k)?.name ?? k;
  return (
    <>
      <Sheet title={t("glossary.definition")}>
        <p className="text-lg leading-7">{x.definition || t("glossary.noDefinition")}</p>
      </Sheet>
      <Sheet title={t("glossary.fields")}>
        <dl className="grid gap-x-6 gap-y-3 sm:grid-cols-[max-content_1fr]">
          <Row label={t("glossary.synonyms")}>{x.synonyms.join(", ") || "—"}</Row>
          <Row label={t("glossary.deprecatedForms")}>{x.deprecatedForms.join(", ") || "—"}</Row>
          <Row label={t("glossary.preferred")}>
            {x.preferred ? (
              <Link to="/glossary/$key" params={{ key: x.preferred }} className="text-accent hover:underline">
                {nameOf(x.preferred)}
              </Link>
            ) : (
              "—"
            )}
          </Row>
          <Row label={t("glossary.owner")}>{x.owner || "—"}</Row>
          <Row label={t("glossary.domain")}>{x.domain || t("glossary.noDomain")}</Row>
          <Row label={t("glossary.related")}>
            <Related entity={entity.data} />
          </Row>
        </dl>
      </Sheet>
      <Sheet title={t("glossary.usage")} description={t("glossary.usageBody")}>
        {x.usage.length === 0 ? (
          <Empty>{t("glossary.noUsage")}</Empty>
        ) : (
          <Table>
            <thead>
              <tr>
                <Th>{t("glossary.connection")}</Th>
                <Th className="text-right">{t("glossary.items")}</Th>
                <Th className="text-right">{t("glossary.deprecatedItems")}</Th>
              </tr>
            </thead>
            <tbody>
              {x.usage.map((u) => (
                <tr key={u.connectionId} data-testid="usage-row">
                  <Td>{u.connectionName}</Td>
                  <Td className="text-right font-mono">{u.items}</Td>
                  <Td className="text-right font-mono">{u.deprecatedItems}</Td>
                </tr>
              ))}
            </tbody>
          </Table>
        )}
      </Sheet>
      <Sheet title={t("glossary.sources")}>
        <Sources entity={entity.data} />
      </Sheet>
    </>
  );
}

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <>
      <dt className="text-xs font-medium tracking-wide text-ink-muted uppercase">{label}</dt>
      <dd>{children}</dd>
    </>
  );
}

function Related({ entity }: { entity?: EntityDetail }) {
  const rel = (entity?.claims ?? []).filter(
    (c) => ["relates_to", "broader"].includes(c.predicate) && c.objectKey,
  );
  if (rel.length === 0) return <>—</>;
  return (
    <ul className="flex flex-wrap gap-2">
      {rel.map((c) => (
        <li key={c.id}>
          <span className="text-xs text-ink-faint">
            {c.predicate === "broader" ? t("glossary.broader") : ""}
          </span>{" "}
          {c.objectName || c.objectKey}
        </li>
      ))}
    </ul>
  );
}

function Sources({ entity }: { entity?: EntityDetail }) {
  const seen = new Map<number, { title: string; url: string; connection: string }>();
  for (const c of entity?.claims ?? []) {
    for (const e of c.evidence) {
      if (!seen.has(e.itemId))
        seen.set(e.itemId, { title: e.title, url: e.url, connection: e.connectionName });
    }
  }
  if (seen.size === 0) return <Empty>{t("glossary.noSources")}</Empty>;
  return (
    <ul className="flex flex-col gap-2">
      {[...seen.entries()].map(([id, s]) => (
        <li key={id} className="flex flex-wrap items-baseline gap-2">
          <a href={s.url} target="_blank" rel="noopener noreferrer" className="text-accent hover:underline">
            {s.title}
          </a>
          <span className="text-xs text-ink-faint">{s.connection}</span>
        </li>
      ))}
    </ul>
  );
}
