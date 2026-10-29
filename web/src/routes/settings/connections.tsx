import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import { ArrowLeft, Plus } from "lucide-react";
import { type FormEvent, useEffect, useMemo, useState } from "react";
import { ApiError, api, type Schemas, unwrap } from "@/api/client";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent } from "@/components/ui/dialog";
import { Field, Input, Select } from "@/components/ui/field";
import { Notice } from "@/components/ui/notice";
import { Empty, Sheet } from "@/components/ui/sheet";
import { SkeletonRows } from "@/components/ui/skeleton";
import { Table, Td, Th } from "@/components/ui/table";
import { type CopyKey, t } from "@/copy";
import { formatAgo, formatTime } from "@/lib/format";

type Connection = Schemas["ConnectionRecord"];
type Run = Schemas["RunRecord"];
type Kind = "confluence" | "jira";

function messageOf(e: unknown): string {
  if (e instanceof ApiError) {
    if (e.code === "name_taken") return t("connections.nameTaken");
    if (e.code === "run_active") return t("connections.runActive");
    return e.message;
  }
  return t("errors.network");
}

const kindLabel = (k: string) => t(`connections.kinds.${k}` as CopyKey);

function StatusBadge({ c }: { c: Pick<Connection, "status"> }) {
  const tone = c.status === "active" ? "supported" : c.status === "paused" ? "danger" : "stale";
  return <Badge tone={tone}>{t(`connections.status.${c.status}` as CopyKey)}</Badge>;
}

function sharedWith(c: Connection): string {
  return c.visibility.length === 0 ? t("connections.ownerOnly") : c.visibility.map((v) => v.name).join(", ");
}

function useConnections() {
  return useQuery({
    queryKey: ["connections"],
    queryFn: async () => unwrap(await api.GET("/api/v1/connections")),
  });
}

export function ConnectionsPage() {
  const list = useConnections();
  const add = (
    <Button asChild variant="primary">
      <Link to="/settings/connections/new">
        <Plus aria-hidden />
        {t("connections.add")}
      </Link>
    </Button>
  );
  return (
    <Sheet title={t("connections.title")} description={t("connections.body")} actions={add}>
      {list.isPending ? (
        <SkeletonRows label={t("app.loading")} rows={3} />
      ) : list.isError ? (
        <Notice tone="danger">{t("app.error")}</Notice>
      ) : (
        <>
          <div className="overflow-x-auto">
            <Table>
              <thead>
                <tr>
                  <Th>{t("connections.colName")}</Th>
                  <Th>{t("connections.colStatus")}</Th>
                  <Th className="text-right">{t("connections.colItems")}</Th>
                  <Th>{t("connections.colSync")}</Th>
                  <Th>{t("connections.colShared")}</Th>
                </tr>
              </thead>
              <tbody>
                {list.data.connections.map((c) => (
                  <tr key={c.id}>
                    <Td>
                      <Link
                        to="/settings/connections/$id"
                        params={{ id: String(c.id) }}
                        className="font-medium text-accent underline-offset-4 hover:underline"
                      >
                        {c.name}
                      </Link>
                      <div className="font-mono text-xs text-ink-muted">{kindLabel(c.kind)}</div>
                    </Td>
                    <Td>
                      <StatusBadge c={c} />
                    </Td>
                    <Td className="text-right font-mono">{c.items}</Td>
                    <Td className="font-mono text-sm">
                      {c.kind === "registry"
                        ? t("connections.none")
                        : c.lastSyncAt
                          ? formatAgo(c.lastSyncAt)
                          : t("connections.never")}
                    </Td>
                    <Td className="text-sm">{sharedWith(c)}</Td>
                  </tr>
                ))}
              </tbody>
            </Table>
          </div>
          {list.data.connections.length <= 1 && <Empty action={add}>{t("connections.empty")}</Empty>}
        </>
      )}
    </Sheet>
  );
}

/** The source part of a connection form. */
type Source = { kind: Kind; name: string; baseUrl: string; token: string };
type Scope = { spaces?: string[]; projects?: string[]; jql?: string };
type Schedule = {
  syncMinutes: number;
  reconcileHours: number;
  rateConcurrency: number;
  ratePerSecond: number;
  defaultDomainId?: number | null;
};

const defaultSchedule: Schedule = {
  syncMinutes: 30,
  reconcileHours: 24,
  rateConcurrency: 4,
  ratePerSecond: 5,
};

function scopeList(kind: Kind, scope: Scope): string[] {
  return (kind === "jira" ? scope.projects : scope.spaces) ?? [];
}

function withList(kind: Kind, scope: Scope, ids: string[]): Scope {
  return kind === "jira" ? { ...scope, projects: ids } : { ...scope, spaces: ids };
}

function SourceFields({
  value,
  onChange,
  fixedKind,
  keepToken,
}: {
  value: Source;
  onChange: (s: Source) => void;
  fixedKind?: boolean;
  keepToken?: boolean;
}) {
  return (
    <div className="grid gap-4 sm:grid-cols-2">
      <Field label={t("connections.kind")}>
        {(p) => (
          <Select
            {...p}
            disabled={fixedKind}
            value={value.kind}
            onChange={(e) => onChange({ ...value, kind: e.target.value as Kind })}
          >
            <option value="confluence">{kindLabel("confluence")}</option>
            <option value="jira">{kindLabel("jira")}</option>
          </Select>
        )}
      </Field>
      <Field label={t("connections.name")} hint={t("connections.nameHint")}>
        {(p) => (
          <Input
            {...p}
            required
            maxLength={100}
            value={value.name}
            onChange={(e) => onChange({ ...value, name: e.target.value })}
          />
        )}
      </Field>
      <Field label={t("connections.baseUrl")} hint={t("connections.baseUrlHint")}>
        {(p) => (
          <Input
            {...p}
            required
            type="url"
            className="font-mono"
            value={value.baseUrl}
            onChange={(e) => onChange({ ...value, baseUrl: e.target.value })}
          />
        )}
      </Field>
      <Field
        label={t("connections.token")}
        hint={keepToken ? t("connections.tokenKeep") : t("connections.tokenHint")}
      >
        {(p) => (
          <Input
            {...p}
            type="password"
            autoComplete="off"
            required={!keepToken}
            className="font-mono"
            value={value.token}
            onChange={(e) => onChange({ ...value, token: e.target.value })}
          />
        )}
      </Field>
    </div>
  );
}

function sourceBody(s: Source, scope: Scope | undefined, connectionId?: number) {
  return {
    kind: s.kind,
    baseUrl: s.baseUrl.trim(),
    token: s.token || undefined,
    connectionId: s.token ? undefined : connectionId,
    scope: scope as Record<string, unknown> | undefined,
  };
}

function useTest() {
  return useMutation({
    mutationFn: async (body: ReturnType<typeof sourceBody>) =>
      unwrap(await api.POST("/api/v1/connections/test", { body })),
  });
}

/** A live list of spaces or projects with checkboxes. */
function ScopePicker({
  source,
  connectionId,
  scope,
  onChange,
}: {
  source: Source;
  connectionId?: number;
  scope: Scope;
  onChange: (s: Scope) => void;
}) {
  const [filter, setFilter] = useState("");
  const options = useQuery({
    queryKey: ["connection-scopes", source.kind, source.baseUrl, connectionId, source.token.length],
    queryFn: async () =>
      unwrap(
        await api.POST("/api/v1/connections/scopes", { body: sourceBody(source, undefined, connectionId) }),
      ).scopes,
    retry: false,
  });
  const selected = scopeList(source.kind, scope);
  const toggle = (id: string) =>
    onChange(
      withList(
        source.kind,
        scope,
        selected.includes(id) ? selected.filter((x) => x !== id) : [...selected, id],
      ),
    );
  const shown = (options.data ?? []).filter((o) =>
    `${o.id} ${o.name}`.toLowerCase().includes(filter.trim().toLowerCase()),
  );
  const label = source.kind === "jira" ? t("connections.projects") : t("connections.spaces");
  return (
    <div className="flex flex-col gap-3">
      <Field label={t("connections.scopeFilter")} hint={t("connections.scopeHint")}>
        {(p) => <Input {...p} value={filter} onChange={(e) => setFilter(e.target.value)} />}
      </Field>
      {options.isPending ? (
        <SkeletonRows label={t("app.loading")} rows={3} />
      ) : options.isError ? (
        <Notice tone="danger">{messageOf(options.error)}</Notice>
      ) : options.data.length === 0 ? (
        <Empty>{t("connections.scopeEmpty")}</Empty>
      ) : (
        <fieldset className="flex flex-col gap-1">
          <legend className="label mb-1">{label}</legend>
          <ul className="max-h-72 overflow-y-auto rounded-sm border border-rule">
            {shown.map((o) => (
              <li key={o.id} className="border-b border-rule last:border-b-0">
                <label className="flex cursor-pointer items-center gap-3 px-3 py-2 transition-ink hover:bg-paper-sunk">
                  <input
                    type="checkbox"
                    checked={selected.includes(o.id)}
                    onChange={() => toggle(o.id)}
                    className="size-4 accent-accent"
                  />
                  <span className="font-mono text-sm">{o.id}</span>
                  <span>{o.name}</span>
                </label>
              </li>
            ))}
          </ul>
        </fieldset>
      )}
      {source.kind === "jira" && (
        <Field label={t("connections.jql")} hint={t("connections.jqlHint")}>
          {(p) => (
            <Input
              {...p}
              className="font-mono"
              value={scope.jql ?? ""}
              onChange={(e) => onChange({ ...scope, jql: e.target.value })}
            />
          )}
        </Field>
      )}
    </div>
  );
}

function ScheduleFields({ value, onChange }: { value: Schedule; onChange: (s: Schedule) => void }) {
  const domains = useQuery({
    queryKey: ["domains"],
    queryFn: async () => unwrap(await api.GET("/api/v1/domains")).domains,
  });
  const num = (key: keyof Schedule, label: CopyKey, min: number, max: number, step = 1) => (
    <Field label={t(label)}>
      {(p) => (
        <Input
          {...p}
          type="number"
          min={min}
          max={max}
          step={step}
          required
          className="font-mono"
          value={Number(value[key] ?? 0)}
          onChange={(e) => onChange({ ...value, [key]: Number(e.target.value) })}
        />
      )}
    </Field>
  );
  return (
    <div className="grid gap-4 sm:grid-cols-2">
      {num("syncMinutes", "connections.syncMinutes", 5, 10080)}
      {num("reconcileHours", "connections.reconcileHours", 1, 720)}
      {num("rateConcurrency", "connections.concurrency", 1, 32)}
      {num("ratePerSecond", "connections.perSecond", 0.1, 100, 0.1)}
      <Field label={t("connections.domain")} hint={t("connections.domainHint")}>
        {(p) => (
          <Select
            {...p}
            value={value.defaultDomainId ?? ""}
            onChange={(e) =>
              onChange({ ...value, defaultDomainId: e.target.value ? Number(e.target.value) : null })
            }
          >
            <option value="">{t("connections.noDomain")}</option>
            {domains.data?.map((d) => (
              <option key={d.id} value={d.id}>
                {d.name}
              </option>
            ))}
          </Select>
        )}
      </Field>
    </div>
  );
}

export function NewConnectionPage() {
  const navigate = useNavigate();
  const qc = useQueryClient();
  const [step, setStep] = useState(1);
  const [source, setSource] = useState<Source>({ kind: "confluence", name: "", baseUrl: "", token: "" });
  const [scope, setScope] = useState<Scope>({});
  const [schedule, setSchedule] = useState<Schedule>(defaultSchedule);
  const [error, setError] = useState<string>();
  const test = useTest();
  const tested = test.isSuccess && test.variables?.baseUrl === source.baseUrl.trim();

  const save = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.POST("/api/v1/connections", {
          body: {
            ...sourceBody(source, scope),
            name: source.name.trim(),
            ...schedule,
            defaultDomainId: schedule.defaultDomainId ?? undefined,
          },
        }),
      ),
    onSuccess: (r) => {
      qc.invalidateQueries({ queryKey: ["connections"] });
      navigate({ to: "/settings/connections/$id", params: { id: String(r.id) } });
    },
    onError: (e) => setError(messageOf(e)),
  });

  const submitSource = (e: FormEvent) => {
    e.preventDefault();
    setError(undefined);
    if (!tested) {
      setError(t("connections.testFirst"));
      return;
    }
    setStep(2);
  };
  const submitScope = (e: FormEvent) => {
    e.preventDefault();
    if (scopeList(source.kind, scope).length === 0) {
      setError(t("connections.scopeRequired"));
      return;
    }
    setError(undefined);
    setStep(3);
  };
  const submitSchedule = (e: FormEvent) => {
    e.preventDefault();
    setError(undefined);
    save.mutate();
  };
  const steps: CopyKey[] = ["connections.stepSource", "connections.stepScope", "connections.stepSchedule"];

  return (
    <div className="flex flex-col gap-4">
      <BackLink />
      <Sheet title={t("connections.wizardTitle")} description={t("connections.stepOf", { step })}>
        <ol className="mb-4 flex flex-wrap gap-4 border-b border-rule pb-3 text-sm">
          {steps.map((s, i) => (
            <li
              key={s}
              aria-current={step === i + 1 ? "step" : undefined}
              className="font-mono text-ink-muted aria-[current=step]:font-semibold aria-[current=step]:text-ink"
            >
              {i + 1}. {t(s)}
            </li>
          ))}
        </ol>
        {error && (
          <div className="mb-4">
            <Notice tone="danger">{error}</Notice>
          </div>
        )}
        {step === 1 && (
          <form onSubmit={submitSource} className="flex flex-col gap-4">
            <SourceFields value={source} onChange={setSource} />
            <div className="flex flex-wrap items-center gap-3">
              <Button
                type="button"
                disabled={test.isPending || !source.baseUrl || !source.token}
                onClick={() => {
                  setError(undefined);
                  test.mutate(sourceBody(source, undefined));
                }}
              >
                {test.isPending ? t("connections.testing") : t("connections.test")}
              </Button>
              <Button type="submit" variant="primary">
                {t("connections.next")}
              </Button>
            </div>
            {test.isSuccess && (
              <Notice tone="success">
                {t("connections.testOk", { user: test.data.user, count: test.data.sample })}
              </Notice>
            )}
            {test.isError && <Notice tone="danger">{messageOf(test.error)}</Notice>}
          </form>
        )}
        {step === 2 && (
          <form onSubmit={submitScope} className="flex flex-col gap-4">
            <ScopePicker source={source} scope={scope} onChange={setScope} />
            <div className="flex gap-3">
              <Button type="button" onClick={() => setStep(1)}>
                {t("connections.previous")}
              </Button>
              <Button type="submit" variant="primary">
                {t("connections.next")}
              </Button>
            </div>
          </form>
        )}
        {step === 3 && (
          <form onSubmit={submitSchedule} className="flex flex-col gap-4">
            <ScheduleFields value={schedule} onChange={setSchedule} />
            <Notice>{t("connections.ownerOnlyNote")}</Notice>
            <div className="flex gap-3">
              <Button type="button" onClick={() => setStep(2)}>
                {t("connections.previous")}
              </Button>
              <Button type="submit" variant="primary" disabled={save.isPending}>
                {t("connections.save")}
              </Button>
            </div>
          </form>
        )}
      </Sheet>
    </div>
  );
}

function BackLink() {
  return (
    <Link
      to="/settings/connections"
      className="inline-flex items-center gap-1 text-sm text-accent underline-offset-4 hover:underline"
    >
      <ArrowLeft aria-hidden className="size-4" />
      {t("connections.back")}
    </Link>
  );
}

export function ConnectionPage() {
  const { id } = useParams({ strict: false }) as { id: string };
  const list = useConnections();
  const c = list.data?.connections.find((x) => String(x.id) === id);
  if (list.isPending) return <SkeletonRows label={t("app.loading")} rows={6} />;
  if (list.isError) return <Notice tone="danger">{t("app.error")}</Notice>;
  if (!c) return <Notice tone="danger">{t("app.notFoundBody")}</Notice>;
  return (
    <div className="flex flex-col gap-6">
      <BackLink />
      <HealthSheet c={c} />
      {c.kind !== "registry" && (
        <>
          <VisibilitySheet key={`v${c.id}`} c={c} />
          <ConfigurationSheet key={`c${c.id}`} c={c} />
          <RunsSheet c={c} />
        </>
      )}
    </div>
  );
}

function Term({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex flex-col gap-0.5">
      <dt className="label">{label}</dt>
      <dd className="font-mono text-sm">{children}</dd>
    </div>
  );
}

function HealthSheet({ c }: { c: Connection }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [msg, setMsg] = useState<{ tone: "success" | "danger"; text: string }>();
  const [purgeOpen, setPurgeOpen] = useState(false);
  const [confirmName, setConfirmName] = useState("");
  const refresh = () => {
    qc.invalidateQueries({ queryKey: ["connections"] });
    qc.invalidateQueries({ queryKey: ["connection-runs", c.id] });
  };
  const start = useMutation({
    mutationFn: async (kind: "sync" | "reconcile") =>
      kind === "sync"
        ? unwrap(await api.POST("/api/v1/connections/{id}/sync", { params: { path: { id: c.id } } }))
        : unwrap(await api.POST("/api/v1/connections/{id}/reconcile", { params: { path: { id: c.id } } })),
    onSuccess: () => {
      setMsg({ tone: "success", text: t("connections.queued") });
      refresh();
    },
    onError: (e) => setMsg({ tone: "danger", text: messageOf(e) }),
  });
  const pause = useMutation({
    mutationFn: async (paused: boolean) =>
      unwrap(
        await api.PUT("/api/v1/connections/{id}/paused", {
          params: { path: { id: c.id } },
          body: { paused },
        }),
      ),
    onSuccess: refresh,
    onError: (e) => setMsg({ tone: "danger", text: messageOf(e) }),
  });
  const purge = useMutation({
    mutationFn: async () =>
      unwrap(await api.DELETE("/api/v1/connections/{id}", { params: { path: { id: c.id } } })),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["connections"] });
      navigate({ to: "/settings/connections" });
    },
    onError: (e) => setMsg({ tone: "danger", text: messageOf(e) }),
  });
  const registry = c.kind === "registry";
  const actions = !registry && (
    <>
      <Button
        size="sm"
        disabled={c.status === "paused" || start.isPending}
        onClick={() => start.mutate("sync")}
      >
        {t("connections.syncNow")}
      </Button>
      <Button
        size="sm"
        disabled={c.status === "paused" || start.isPending}
        onClick={() => start.mutate("reconcile")}
      >
        {t("connections.reconcileNow")}
      </Button>
      <Button size="sm" variant="ghost" onClick={() => pause.mutate(c.status !== "paused")}>
        {c.status === "paused" ? t("connections.resume") : t("connections.pause")}
      </Button>
      <Button size="sm" variant="danger" onClick={() => setPurgeOpen(true)}>
        {t("connections.purge")}
      </Button>
    </>
  );
  return (
    <Sheet title={c.name} description={kindLabel(c.kind)} actions={actions}>
      <div className="flex flex-col gap-4">
        {msg && <Notice tone={msg.tone}>{msg.text}</Notice>}
        {registry && <Notice>{t("connections.registryHelp")}</Notice>}
        {c.status !== "active" && c.statusReason && <Notice tone="danger">{c.statusReason}</Notice>}
        <dl className="grid gap-4 sm:grid-cols-3">
          <Term label={t("connections.colStatus")}>
            <StatusBadge c={c} />
          </Term>
          <Term label={t("connections.items")}>{c.items}</Term>
          <Term label={t("connections.owner")}>{c.ownerEmail || t("connections.none")}</Term>
          {!registry && (
            <>
              <Term label={t("connections.lastSync")}>
                {c.lastSyncAt ? formatTime(c.lastSyncAt) : t("connections.never")}
              </Term>
              <Term label={t("connections.nextSync")}>
                {c.nextSyncAt ? formatTime(c.nextSyncAt) : t("connections.none")}
              </Term>
              <Term label={t("connections.lastReconcile")}>
                {c.lastReconcileAt ? formatTime(c.lastReconcileAt) : t("connections.never")}
              </Term>
            </>
          )}
        </dl>
        {!registry && (
          <p className="text-sm" data-testid="permissions-checked">
            {c.permissionsCheckedAt
              ? t("connections.permissionsChecked", { ago: formatAgo(c.permissionsCheckedAt) })
              : t("connections.permissionsNever")}
          </p>
        )}
        {c.lastError && (
          <div>
            <p className="label">{t("connections.lastError")}</p>
            <p className="text-sm text-danger">{c.lastError}</p>
          </div>
        )}
      </div>
      <Dialog open={purgeOpen} onOpenChange={setPurgeOpen}>
        <DialogContent
          title={t("connections.purgeTitle", { name: c.name })}
          closeLabel={t("connections.cancel")}
        >
          <p>{t("connections.purgeBody")}</p>
          <Field label={t("connections.purgeConfirm")}>
            {(p) => <Input {...p} value={confirmName} onChange={(e) => setConfirmName(e.target.value)} />}
          </Field>
          <div className="flex justify-end gap-2">
            <Button onClick={() => setPurgeOpen(false)}>{t("connections.cancel")}</Button>
            <Button
              variant="danger"
              disabled={confirmName !== c.name || purge.isPending}
              onClick={() => purge.mutate()}
            >
              {t("connections.purgeAction")}
            </Button>
          </div>
        </DialogContent>
      </Dialog>
    </Sheet>
  );
}

function VisibilitySheet({ c }: { c: Connection }) {
  const qc = useQueryClient();
  const groups = useQuery({
    queryKey: ["groups"],
    queryFn: async () => unwrap(await api.GET("/api/v1/groups")).groups,
  });
  const current = useMemo(() => c.visibility.map((v) => v.id), [c.visibility]);
  const [picked, setPicked] = useState<number[]>(current);
  const [confirmed, setConfirmed] = useState(false);
  const [msg, setMsg] = useState<{ tone: "success" | "danger"; text: string }>();
  useEffect(() => setPicked(current), [current]);
  const adding = picked.some((g) => !current.includes(g));
  const save = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.PUT("/api/v1/connections/{id}/visibility", {
          params: { path: { id: c.id } },
          body: { groupIds: picked, confirm: confirmed },
        }),
      ),
    onSuccess: () => {
      setConfirmed(false);
      setMsg({ tone: "success", text: t("connections.visibilitySaved") });
      qc.invalidateQueries({ queryKey: ["connections"] });
    },
    onError: (e) => setMsg({ tone: "danger", text: messageOf(e) }),
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    setMsg(undefined);
    if (adding && !confirmed) {
      setMsg({ tone: "danger", text: t("connections.confirmRequired") });
      return;
    }
    save.mutate();
  };
  return (
    <Sheet title={t("connections.visibility")} description={t("connections.visibilityBody")}>
      <form onSubmit={submit} className="flex flex-col gap-4">
        {msg && <Notice tone={msg.tone}>{msg.text}</Notice>}
        {groups.isPending ? (
          <SkeletonRows label={t("app.loading")} rows={2} />
        ) : (
          <fieldset>
            <legend className="label mb-1">{t("connections.colShared")}</legend>
            <ul className="flex flex-col gap-1">
              {groups.data?.map((g) => (
                <li key={g.id}>
                  <label className="flex cursor-pointer items-center gap-3">
                    <input
                      type="checkbox"
                      className="size-4 accent-accent"
                      checked={picked.includes(g.id)}
                      onChange={() =>
                        setPicked(
                          picked.includes(g.id) ? picked.filter((x) => x !== g.id) : [...picked, g.id],
                        )
                      }
                    />
                    <span>{g.name}</span>
                  </label>
                </li>
              ))}
            </ul>
          </fieldset>
        )}
        {adding && (
          <label className="flex cursor-pointer items-start gap-3 rounded-sm border border-stale bg-wash-stale px-3 py-2">
            <input
              type="checkbox"
              className="mt-1 size-4 accent-accent"
              checked={confirmed}
              onChange={(e) => setConfirmed(e.target.checked)}
            />
            <span>{t("connections.confirmShare")}</span>
          </label>
        )}
        <div>
          <Button type="submit" variant="primary" disabled={save.isPending}>
            {t("connections.saveVisibility")}
          </Button>
        </div>
      </form>
    </Sheet>
  );
}

function ConfigurationSheet({ c }: { c: Connection }) {
  const qc = useQueryClient();
  const kind = c.kind as Kind;
  const [source, setSource] = useState<Source>({ kind, name: c.name, baseUrl: c.baseUrl, token: "" });
  const [scope, setScope] = useState<Scope>(c.scope as Scope);
  const [schedule, setSchedule] = useState<Schedule>({
    syncMinutes: c.syncMinutes,
    reconcileHours: c.reconcileHours,
    rateConcurrency: c.rateConcurrency,
    ratePerSecond: c.ratePerSecond,
    defaultDomainId: c.defaultDomainId ?? null,
  });
  const [msg, setMsg] = useState<{ tone: "success" | "danger"; text: string }>();
  const test = useTest();
  const save = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.PUT("/api/v1/connections/{id}", {
          params: { path: { id: c.id } },
          body: {
            kind,
            name: source.name.trim(),
            baseUrl: source.baseUrl.trim(),
            token: source.token || undefined,
            scope: scope as Record<string, unknown>,
            ...schedule,
            defaultDomainId: schedule.defaultDomainId ?? undefined,
          },
        }),
      ),
    onSuccess: () => {
      setSource({ ...source, token: "" });
      setMsg({ tone: "success", text: t("connections.configurationSaved") });
      qc.invalidateQueries({ queryKey: ["connections"] });
    },
    onError: (e) => setMsg({ tone: "danger", text: messageOf(e) }),
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    setMsg(undefined);
    if (scopeList(kind, scope).length === 0) {
      setMsg({ tone: "danger", text: t("connections.scopeRequired") });
      return;
    }
    save.mutate();
  };
  return (
    <Sheet title={t("connections.configuration")}>
      <form onSubmit={submit} className="flex flex-col gap-6">
        {msg && <Notice tone={msg.tone}>{msg.text}</Notice>}
        <SourceFields value={source} onChange={setSource} fixedKind keepToken />
        <div className="flex flex-wrap items-center gap-3">
          <Button
            type="button"
            disabled={test.isPending}
            onClick={() => test.mutate(sourceBody(source, scope, c.id))}
          >
            {test.isPending ? t("connections.testing") : t("connections.test")}
          </Button>
          {test.isSuccess && (
            <Notice tone="success">
              {t("connections.testScopeOk", { user: test.data.user, count: test.data.sample })}
            </Notice>
          )}
          {test.isError && <Notice tone="danger">{messageOf(test.error)}</Notice>}
        </div>
        <ScopePicker source={source} connectionId={c.id} scope={scope} onChange={setScope} />
        <ScheduleFields value={schedule} onChange={setSchedule} />
        <div>
          <Button type="submit" variant="primary" disabled={save.isPending}>
            {t("connections.saveConfiguration")}
          </Button>
        </div>
      </form>
    </Sheet>
  );
}

function runTone(r: Run) {
  if (r.status === "succeeded") return "supported" as const;
  if (r.status === "failed") return "danger" as const;
  return "stale" as const;
}

function RunsSheet({ c }: { c: Connection }) {
  const runs = useQuery({
    queryKey: ["connection-runs", c.id],
    queryFn: async () =>
      unwrap(await api.GET("/api/v1/connections/{id}/runs", { params: { path: { id: c.id } } })).runs,
    refetchInterval: (q) =>
      q.state.data?.some((r) => r.status === "queued" || r.status === "running") ? 2000 : false,
  });
  const qc = useQueryClient();
  const active = runs.data?.some((r) => r.status === "queued" || r.status === "running");
  useEffect(() => {
    if (active === false) qc.invalidateQueries({ queryKey: ["connections"] });
  }, [active, qc]);
  return (
    <Sheet title={t("connections.runs")}>
      {runs.isPending ? (
        <SkeletonRows label={t("app.loading")} rows={3} />
      ) : runs.isError ? (
        <Notice tone="danger">{t("app.error")}</Notice>
      ) : runs.data.length === 0 ? (
        <Empty>{t("connections.runsEmpty")}</Empty>
      ) : (
        <div className="overflow-x-auto">
          <Table>
            <thead>
              <tr>
                <Th>{t("connections.colRun")}</Th>
                <Th>{t("connections.colResult")}</Th>
                <Th>{t("connections.colCounts")}</Th>
                <Th>{t("connections.colWhen")}</Th>
              </tr>
            </thead>
            <tbody>
              {runs.data.map((r) => (
                <tr key={r.id} data-testid="run-row">
                  <Td>
                    <div className="font-medium">{t(`connections.run.${r.kind}` as CopyKey)}</div>
                    <div className="font-mono text-xs text-ink-muted">
                      {t(`connections.run.${r.trigger}` as CopyKey)}
                    </div>
                  </Td>
                  <Td>
                    <Badge tone={runTone(r)}>{t(`connections.run.${r.status}` as CopyKey)}</Badge>
                    {r.error && <p className="mt-1 text-sm text-danger">{r.error}</p>}
                  </Td>
                  <Td className="font-mono text-sm">
                    {t("connections.counts", {
                      created: r.created,
                      updated: r.updated,
                      unchanged: r.unchanged,
                      deleted: r.deleted,
                    })}
                    {r.skipped.length > 0 && (
                      <details className="mt-1">
                        <summary className="cursor-pointer">
                          {t("connections.skipped", { count: r.skipped.length })}
                        </summary>
                        <ul>
                          {r.skipped.map((s) => (
                            <li key={s.id}>
                              {s.id}: {s.reason}
                            </li>
                          ))}
                        </ul>
                      </details>
                    )}
                  </Td>
                  <Td className="font-mono text-sm">{formatTime(r.queuedAt)}</Td>
                </tr>
              ))}
            </tbody>
          </Table>
        </div>
      )}
    </Sheet>
  );
}
