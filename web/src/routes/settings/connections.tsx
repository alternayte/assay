import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import { ArrowLeft, Plus, Trash2 } from "lucide-react";
import { type FormEvent, useEffect, useMemo, useState } from "react";
import { ApiError, api, type Schemas, unwrap } from "@/api/client";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent } from "@/components/ui/dialog";
import { Field, Input, Select, Textarea } from "@/components/ui/field";
import { Notice } from "@/components/ui/notice";
import { Empty, Sheet } from "@/components/ui/sheet";
import { SkeletonRows } from "@/components/ui/skeleton";
import { Table, Td, Th } from "@/components/ui/table";
import { type CopyKey, t } from "@/copy";
import { formatAgo, formatTime } from "@/lib/format";

type Connection = Schemas["ConnectionRecord"];
type Run = Schemas["RunRecord"];
type Kind = "confluence" | "jira" | "github" | "azdo" | "gitlab" | "bitbucket" | "backstage" | "web";

const repoKinds: string[] = ["github", "azdo", "gitlab", "bitbucket"];
const isRepo = (k: string) => repoKinds.includes(k);

/** GitLab and Bitbucket name the token scopes in their own hint. */
function tokenHint(kind: Kind): string {
  if (kind === "gitlab" || kind === "bitbucket") return t(`connections.tokenHints.${kind}`);
  return t("connections.tokenHint");
}
const isAtlassian = (k: string) => k === "jira" || k === "confluence";
type Deployment = "dc" | "cloud";
/** A site on atlassian.net is Cloud unless the admin selects otherwise. */
const deploymentFor = (baseUrl: string): Deployment => {
  try {
    return new URL(baseUrl.trim()).hostname.toLowerCase().endsWith(".atlassian.net") ? "cloud" : "dc";
  } catch {
    return "dc";
  }
};

function messageOf(e: unknown): string {
  if (e instanceof ApiError) {
    if (e.code === "name_taken") return t("connections.nameTaken");
    if (e.code === "run_active") return t("connections.runActive");
    return e.message;
  }
  return t("errors.network");
}

const kindLabel = (k: string) => t(`connections.kinds.${k}` as CopyKey);

/** The kind with the deployment of a Jira or Confluence connection. */
const sourceLabel = (c: Pick<Connection, "kind" | "deployment">) =>
  c.deployment
    ? `${kindLabel(c.kind)}, ${t(`connections.deployments.${c.deployment}` as CopyKey)}`
    : kindLabel(c.kind);

function StatusBadge({ c }: { c: Pick<Connection, "status"> }) {
  const tone = c.status === "active" ? "supported" : "danger";
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
    <div className="flex flex-col gap-6">
      <DemoNotice />
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
                        <div className="font-mono text-xs text-ink-muted">{sourceLabel(c)}</div>
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
    </div>
  );
}

/** While the Acme demo connections exist, admins can remove them with their data in one step. */
function DemoNotice() {
  const qc = useQueryClient();
  const [asking, setAsking] = useState(false);
  const demo = useQuery({
    queryKey: ["demo"],
    queryFn: async () => unwrap(await api.GET("/api/v1/demo")).connections,
  });
  const remove = useMutation({
    mutationFn: async () => unwrap(await api.POST("/api/v1/demo/remove")).connections,
    onSuccess: () => {
      setAsking(false);
      qc.invalidateQueries();
    },
  });
  if (remove.isSuccess) {
    return <Notice tone="success">{t("connections.demoRemoved", { names: remove.data.join(", ") })}</Notice>;
  }
  const names = demo.data ?? [];
  if (names.length === 0) return null;
  return (
    <div className="flex flex-col gap-3 rounded-md border border-rule bg-paper-raised p-4 shadow-sheet sm:flex-row sm:items-center sm:justify-between">
      <div className="flex flex-col gap-1">
        <p className="font-medium">{t("connections.demoTitle")}</p>
        <p className="text-base text-ink-muted">{t("connections.demoBody", { names: names.join(", ") })}</p>
      </div>
      <Button onClick={() => setAsking(true)} className="shrink-0">
        <Trash2 aria-hidden />
        {t("connections.demoRemove")}
      </Button>
      {asking && (
        <Dialog open onOpenChange={(o) => !o && setAsking(false)}>
          <DialogContent
            title={t("connections.demoConfirmTitle")}
            description={t("connections.demoConfirmBody", { names: names.join(", ") })}
            closeLabel={t("connections.cancel")}
          >
            {remove.isError && <Notice tone="danger">{messageOf(remove.error)}</Notice>}
            <div className="flex gap-3">
              <Button onClick={() => setAsking(false)}>{t("connections.cancel")}</Button>
              <Button variant="danger" disabled={remove.isPending} onClick={() => remove.mutate()}>
                <Trash2 aria-hidden />
                {t("connections.demoRemove")}
              </Button>
            </div>
          </DialogContent>
        </Dialog>
      )}
    </div>
  );
}

/** The source part of a connection form. */
type Source = {
  kind: Kind;
  name: string;
  baseUrl: string;
  token: string;
  apiVersion?: string;
  frontendUrl?: string;
  /** Jira and Confluence: Data Center or Cloud. Unset follows the base URL. */
  deployment?: Deployment;
  email?: string;
  scoped?: boolean;
  /** The auth header name of a web site. The token is its value. */
  authName?: string;
};

const deploymentOf = (s: Source): Deployment => s.deployment ?? deploymentFor(s.baseUrl);
const isCloud = (s: Source) => isAtlassian(s.kind) && deploymentOf(s) === "cloud";
type Seed = "auto" | "llms" | "llms-full" | "sitemap" | "start";
type Scope = {
  spaces?: string[];
  projects?: string[];
  repositories?: string[];
  namespaces?: string[];
  jql?: string;
  seed?: Seed;
  pathPrefixes?: string[];
  maxDepth?: number;
  maxPages?: number;
};
/** Repository globs, one per line. */
type Globs = { include: string; exclude: string; code: string };
type Schedule = {
  syncMinutes: number;
  reconcileHours: number;
  rateConcurrency: number;
  ratePerSecond: number;
  parallelParts: number;
  defaultDomainId?: number | null;
};

/** The default rate of an Atlassian Cloud connection. */
const cloudRate = 10;

const defaultSchedule: Schedule = {
  syncMinutes: 30,
  reconcileHours: 24,
  rateConcurrency: 4,
  ratePerSecond: 5,
  parallelParts: 4,
};

function scopeList(kind: Kind, scope: Scope): string[] {
  if (isRepo(kind)) return scope.repositories ?? [];
  if (kind === "backstage") return scope.namespaces ?? [];
  return (kind === "jira" ? scope.projects : scope.spaces) ?? [];
}

function withList(kind: Kind, scope: Scope, ids: string[]): Scope {
  if (isRepo(kind)) return { ...scope, repositories: ids };
  if (kind === "backstage") return { ...scope, namespaces: ids };
  return kind === "jira" ? { ...scope, projects: ids } : { ...scope, spaces: ids };
}

/**
 * A Backstage connection with no namespace reads all namespaces. A web
 * connection reads from its seed.
 */
const needsScope = (kind: Kind) => kind !== "backstage" && kind !== "web";

/** A web site needs no token. Its optional auth header is the token. */
const needsToken = (kind: Kind) => kind !== "web";

function tokenOf(s: Source): string | undefined {
  if (!s.token) return undefined;
  if (s.kind === "web") return `${s.authName?.trim() ?? ""}: ${s.token}`;
  return s.token;
}

function optionsOf(s: Source): Record<string, unknown> | undefined {
  if (s.kind === "azdo") return { apiVersion: s.apiVersion?.trim() || undefined };
  if (s.kind === "backstage") return { frontendUrl: s.frontendUrl?.trim() || undefined };
  if (isAtlassian(s.kind)) {
    if (!isCloud(s)) return { deployment: "dc" };
    return { deployment: "cloud", email: s.email?.trim() ?? "", scoped: s.scoped ?? false };
  }
  return undefined;
}

/** The copy of a successful test: a web site counts the pages of its seed. */
const testCopy = (kind: Kind, other: CopyKey): CopyKey => (kind === "web" ? "connections.testWebOk" : other);

const lines = (s: string) =>
  s
    .split("\n")
    .map((l) => l.trim())
    .filter(Boolean);

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
            <option value="github">{kindLabel("github")}</option>
            <option value="azdo">{kindLabel("azdo")}</option>
            <option value="gitlab">{kindLabel("gitlab")}</option>
            <option value="bitbucket">{kindLabel("bitbucket")}</option>
            <option value="backstage">{kindLabel("backstage")}</option>
            <option value="web">{kindLabel("web")}</option>
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
      {isAtlassian(value.kind) && (
        <Field
          label={t("connections.deployment")}
          hint={fixedKind ? t("connections.deploymentFixedHint") : t("connections.deploymentHint")}
        >
          {(p) => (
            <Select
              {...p}
              disabled={fixedKind}
              value={deploymentOf(value)}
              onChange={(e) => onChange({ ...value, deployment: e.target.value as Deployment })}
            >
              <option value="dc">{t("connections.deployments.dc")}</option>
              <option value="cloud">{t("connections.deployments.cloud")}</option>
            </Select>
          )}
        </Field>
      )}
      <Field
        label={t("connections.baseUrl")}
        hint={
          isCloud(value)
            ? t("connections.cloudUrlHint")
            : t(`connections.baseUrlHints.${value.kind}` as CopyKey)
        }
      >
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
      {isCloud(value) && (
        <Field label={t("connections.email")} hint={t("connections.emailHint")}>
          {(p) => (
            <Input
              {...p}
              required
              type="email"
              autoComplete="off"
              value={value.email ?? ""}
              onChange={(e) => onChange({ ...value, email: e.target.value })}
            />
          )}
        </Field>
      )}
      {value.kind === "web" && (
        <Field label={t("connections.authName")} hint={t("connections.authNameHint")}>
          {(p) => (
            <Input
              {...p}
              autoComplete="off"
              required={value.token !== ""}
              className="font-mono"
              value={value.authName ?? ""}
              onChange={(e) => onChange({ ...value, authName: e.target.value })}
            />
          )}
        </Field>
      )}
      <Field
        label={
          isCloud(value)
            ? t("connections.apiToken")
            : value.kind === "web"
              ? t("connections.authValue")
              : t("connections.token")
        }
        hint={
          keepToken
            ? t("connections.tokenKeep")
            : isCloud(value)
              ? t("connections.apiTokenHint")
              : value.kind === "web"
                ? t("connections.authValueHint")
                : tokenHint(value.kind)
        }
      >
        {(p) => (
          <Input
            {...p}
            type="password"
            autoComplete="off"
            required={!keepToken && needsToken(value.kind)}
            className="font-mono"
            value={value.token}
            onChange={(e) => onChange({ ...value, token: e.target.value })}
          />
        )}
      </Field>
      {isCloud(value) && (
        <label className="flex cursor-pointer items-start gap-3 sm:col-span-2">
          <input
            type="checkbox"
            className="mt-1 size-4 accent-accent"
            checked={value.scoped ?? false}
            onChange={(e) => onChange({ ...value, scoped: e.target.checked })}
          />
          <span className="flex flex-col gap-0.5">
            <span>{t("connections.scoped")}</span>
            <span className="text-sm text-ink-muted">{t("connections.scopedHint")}</span>
          </span>
        </label>
      )}
      {value.kind === "backstage" && (
        <Field label={t("connections.frontendUrl")} hint={t("connections.frontendUrlHint")}>
          {(p) => (
            <Input
              {...p}
              type="url"
              className="font-mono"
              value={value.frontendUrl ?? ""}
              onChange={(e) => onChange({ ...value, frontendUrl: e.target.value })}
            />
          )}
        </Field>
      )}
      {value.kind === "azdo" && (
        <Field label={t("connections.apiVersion")} hint={t("connections.apiVersionHint")}>
          {(p) => (
            <Input
              {...p}
              required
              className="font-mono"
              value={value.apiVersion ?? ""}
              onChange={(e) => onChange({ ...value, apiVersion: e.target.value })}
            />
          )}
        </Field>
      )}
    </div>
  );
}

/** The include and exclude globs of a repository connection. */
function GlobFields({ value, onChange }: { value: Globs; onChange: (g: Globs) => void }) {
  return (
    <div className="grid gap-4 sm:grid-cols-2">
      <Field label={t("connections.includeGlobs")} hint={t("connections.includeGlobsHint")}>
        {(p) => (
          <Textarea
            {...p}
            rows={8}
            className="font-mono text-sm"
            value={value.include}
            onChange={(e) => onChange({ ...value, include: e.target.value })}
          />
        )}
      </Field>
      <Field label={t("connections.excludeGlobs")} hint={t("connections.excludeGlobsHint")}>
        {(p) => (
          <Textarea
            {...p}
            rows={8}
            className="font-mono text-sm"
            value={value.exclude}
            onChange={(e) => onChange({ ...value, exclude: e.target.value })}
          />
        )}
      </Field>
      <Field
        label={t("connections.codeGlobs")}
        hint={t("connections.codeGlobsHint")}
        className="sm:col-span-2"
      >
        {(p) => (
          <Textarea
            {...p}
            rows={5}
            className="font-mono text-sm"
            value={value.code}
            onChange={(e) => onChange({ ...value, code: e.target.value })}
          />
        )}
      </Field>
    </div>
  );
}

function sourceBody(s: Source, scope: Scope | undefined, connectionId?: number, globs?: Globs) {
  return {
    kind: s.kind,
    baseUrl: s.baseUrl.trim(),
    token: tokenOf(s),
    connectionId: s.token ? undefined : connectionId,
    scope: scope as Record<string, unknown> | undefined,
    options: optionsOf(s),
    includeGlobs: globs && isRepo(s.kind) ? lines(globs.include) : undefined,
    excludeGlobs: globs && isRepo(s.kind) ? lines(globs.exclude) : undefined,
    codeGlobs: globs && isRepo(s.kind) ? lines(globs.code) : undefined,
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
    queryKey: [
      "connection-scopes",
      source.kind,
      source.baseUrl,
      source.apiVersion,
      deploymentOf(source),
      source.email,
      source.scoped,
      connectionId,
      source.token.length,
    ],
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
  const allShown = shown.length > 0 && shown.every((o) => selected.includes(o.id));
  const someShown = shown.some((o) => selected.includes(o.id));
  const toggleShown = () => {
    const ids = new Set(shown.map((o) => o.id));
    onChange(
      withList(
        source.kind,
        scope,
        allShown ? selected.filter((x) => !ids.has(x)) : [...new Set([...selected, ...ids])],
      ),
    );
  };
  const label = isRepo(source.kind)
    ? t("connections.repositories")
    : source.kind === "backstage"
      ? t("connections.namespaces")
      : source.kind === "jira"
        ? t("connections.projects")
        : t("connections.spaces");
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
        <fieldset className="flex min-w-0 flex-col gap-1">
          <legend className="label mb-1">{label}</legend>
          <div className="flex items-center justify-between gap-3 rounded-t-sm border border-b-0 border-rule bg-paper-sunk px-3 py-2">
            <label className="flex min-w-0 cursor-pointer items-center gap-3">
              <input
                type="checkbox"
                checked={allShown}
                ref={(el) => {
                  if (el) el.indeterminate = someShown && !allShown;
                }}
                onChange={toggleShown}
                disabled={shown.length === 0}
                className="size-4 shrink-0 accent-accent"
              />
              <span className="truncate">{t("connections.scopeAll", { count: shown.length })}</span>
            </label>
            <span className="shrink-0 text-sm text-ink-muted">
              {t("connections.scopeSelected", { count: selected.length })}
            </span>
          </div>
          <ul className="max-h-72 overflow-y-auto rounded-b-sm border border-rule">
            {shown.map((o) => (
              <li key={o.id} className="border-b border-rule last:border-b-0">
                <label
                  title={o.name && o.name !== o.id ? `${o.id} ${o.name}` : o.id}
                  className="flex min-w-0 cursor-pointer items-center gap-3 px-3 py-2 transition-ink hover:bg-paper-sunk"
                >
                  <input
                    type="checkbox"
                    checked={selected.includes(o.id)}
                    onChange={() => toggle(o.id)}
                    className="size-4 shrink-0 accent-accent"
                  />
                  <span className="max-w-[60%] shrink-0 truncate font-mono text-sm">{o.id}</span>
                  {o.name && o.name !== o.id && <span className="min-w-0 truncate">{o.name}</span>}
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

const seeds: Seed[] = ["auto", "llms", "llms-full", "sitemap", "start"];

/** The seed, the path prefixes and the limits of a web site. */
function WebScopeFields({ value, onChange }: { value: Scope; onChange: (s: Scope) => void }) {
  const [prefixes, setPrefixes] = useState((value.pathPrefixes ?? []).join("\n"));
  return (
    <div className="grid gap-4 sm:grid-cols-2">
      <Field label={t("connections.seed")} hint={t("connections.seedHint")}>
        {(p) => (
          <Select
            {...p}
            value={value.seed ?? "auto"}
            onChange={(e) => onChange({ ...value, seed: e.target.value as Seed })}
          >
            {seeds.map((s) => (
              <option key={s} value={s}>
                {t(`connections.seeds.${s}` as CopyKey)}
              </option>
            ))}
          </Select>
        )}
      </Field>
      <Field label={t("connections.pathPrefixes")} hint={t("connections.pathPrefixesHint")}>
        {(p) => (
          <Textarea
            {...p}
            rows={3}
            className="font-mono text-sm"
            value={prefixes}
            onChange={(e) => {
              setPrefixes(e.target.value);
              onChange({ ...value, pathPrefixes: lines(e.target.value) });
            }}
          />
        )}
      </Field>
      <Field label={t("connections.maxDepth")} hint={t("connections.maxDepthHint")}>
        {(p) => (
          <Input
            {...p}
            type="number"
            min={0}
            max={20}
            required
            className="font-mono"
            value={value.maxDepth ?? 3}
            onChange={(e) => onChange({ ...value, maxDepth: Number(e.target.value) })}
          />
        )}
      </Field>
      <Field label={t("connections.maxPages")} hint={t("connections.maxPagesHint")}>
        {(p) => (
          <Input
            {...p}
            type="number"
            min={1}
            max={100000}
            required
            className="font-mono"
            value={value.maxPages ?? 2000}
            onChange={(e) => onChange({ ...value, maxPages: Number(e.target.value) })}
          />
        )}
      </Field>
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
      {num("parallelParts", "connections.parallelParts", 1, 32)}
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
  const [source, setSource] = useState<Source>({
    kind: "confluence",
    name: "",
    baseUrl: "",
    token: "",
    apiVersion: "7.1",
  });
  const [scope, setScope] = useState<Scope>({});
  const list = useConnections();
  const [globs, setGlobs] = useState<Globs>();
  const defaultGlobs = list.data?.defaultGlobs;
  const defaultCode = list.data?.defaultCodeGlobs;
  useEffect(() => {
    if (defaultGlobs && defaultCode && !globs)
      setGlobs({ include: defaultGlobs.join("\n"), exclude: "", code: defaultCode.join("\n") });
  }, [defaultGlobs, defaultCode, globs]);
  const [schedule, setSchedule] = useState<Schedule>(defaultSchedule);
  // Atlassian Cloud takes 10 requests per second by default, Data Center 5.
  const cloud = isCloud(source);
  useEffect(() => {
    setSchedule((s) => {
      if (cloud && s.ratePerSecond === defaultSchedule.ratePerSecond)
        return { ...s, ratePerSecond: cloudRate };
      if (!cloud && s.ratePerSecond === cloudRate)
        return { ...s, ratePerSecond: defaultSchedule.ratePerSecond };
      return s;
    });
  }, [cloud]);
  const [error, setError] = useState<string>();
  const test = useTest();
  const tested = test.isSuccess && test.variables?.baseUrl === source.baseUrl.trim();

  const save = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.POST("/api/v1/connections", {
          body: {
            ...sourceBody(source, scope, undefined, globs),
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
    if (needsScope(source.kind) && scopeList(source.kind, scope).length === 0) {
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
                disabled={test.isPending || !source.baseUrl || (needsToken(source.kind) && !source.token)}
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
                {t(
                  testCopy(
                    source.kind,
                    isRepo(source.kind)
                      ? "connections.testRepoOk"
                      : test.data.more
                        ? "connections.testOkMore"
                        : "connections.testOk",
                  ),
                  {
                    user: test.data.user,
                    count: test.data.sample,
                  },
                )}
              </Notice>
            )}
            {test.isError && <Notice tone="danger">{messageOf(test.error)}</Notice>}
          </form>
        )}
        {step === 2 && (
          <form onSubmit={submitScope} className="flex flex-col gap-4">
            {source.kind === "web" ? (
              <WebScopeFields value={scope} onChange={setScope} />
            ) : (
              <ScopePicker source={source} scope={scope} onChange={setScope} />
            )}
            {isRepo(source.kind) && globs && <GlobFields value={globs} onChange={setGlobs} />}
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
          <ExtractionSheet c={c} />
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
  const [switchOpen, setSwitchOpen] = useState(false);
  const canSwitch = c.deployment === "dc" && c.status !== "paused";
  const actions = !registry && (
    <>
      {canSwitch && (
        <Button size="sm" onClick={() => setSwitchOpen(true)}>
          {t("connections.switchCloud")}
        </Button>
      )}
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
    <Sheet title={c.name} description={sourceLabel(c)} actions={actions}>
      <div className="flex flex-col gap-4">
        {msg && <Notice tone={msg.tone}>{msg.text}</Notice>}
        {c.switching && <Notice>{t("connections.switchPending")}</Notice>}
        {registry && <Notice>{t("connections.registryHelp")}</Notice>}
        {c.status === "paused" && (
          <Notice tone="danger">
            {[c.statusReason, t("connections.pausedHelp")].filter(Boolean).join(" ")}
          </Notice>
        )}
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
      {switchOpen && (
        <SwitchDialog
          c={c}
          onClose={() => setSwitchOpen(false)}
          onDone={() => {
            setSwitchOpen(false);
            setMsg({ tone: "success", text: t("connections.switchQueued") });
            refresh();
          }}
        />
      )}
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

/** The switch of a Data Center connection to Cloud: the site, the account email and an API token. */
function SwitchDialog({ c, onClose, onDone }: { c: Connection; onClose: () => void; onDone: () => void }) {
  const [site, setSite] = useState("");
  const [email, setEmail] = useState("");
  const [token, setToken] = useState("");
  const [scoped, setScoped] = useState(false);
  const run = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.POST("/api/v1/connections/{id}/cloud", {
          params: { path: { id: c.id } },
          body: { baseUrl: site.trim(), email: email.trim(), token, scoped },
        }),
      ),
    onSuccess: onDone,
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    run.mutate();
  };
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent
        title={t("connections.switchTitle", { name: c.name })}
        description={t("connections.switchBody")}
        closeLabel={t("connections.cancel")}
      >
        <form onSubmit={submit} className="flex flex-col gap-4">
          <Notice>{t("connections.switchKeeps")}</Notice>
          {run.isError && <Notice tone="danger">{messageOf(run.error)}</Notice>}
          <Field label={t("connections.baseUrl")} hint={t("connections.cloudUrlHint")}>
            {(p) => (
              <Input
                {...p}
                required
                type="url"
                className="font-mono"
                value={site}
                onChange={(e) => setSite(e.target.value)}
              />
            )}
          </Field>
          <Field label={t("connections.email")} hint={t("connections.emailHint")}>
            {(p) => (
              <Input
                {...p}
                required
                type="email"
                autoComplete="off"
                value={email}
                onChange={(e) => setEmail(e.target.value)}
              />
            )}
          </Field>
          <Field label={t("connections.apiToken")} hint={t("connections.apiTokenHint")}>
            {(p) => (
              <Input
                {...p}
                required
                type="password"
                autoComplete="off"
                className="font-mono"
                value={token}
                onChange={(e) => setToken(e.target.value)}
              />
            )}
          </Field>
          <label className="flex cursor-pointer items-start gap-3">
            <input
              type="checkbox"
              className="mt-1 size-4 accent-accent"
              checked={scoped}
              onChange={(e) => setScoped(e.target.checked)}
            />
            <span className="flex flex-col gap-0.5">
              <span>{t("connections.scoped")}</span>
              <span className="text-sm text-ink-muted">{t("connections.scopedHint")}</span>
            </span>
          </label>
          <div className="flex justify-end gap-2">
            <Button type="button" onClick={onClose}>
              {t("connections.cancel")}
            </Button>
            <Button type="submit" variant="primary" disabled={run.isPending}>
              {run.isPending ? t("connections.switching") : t("connections.switchAction")}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
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
  const [source, setSource] = useState<Source>({
    kind,
    name: c.name,
    baseUrl: c.baseUrl,
    token: "",
    apiVersion: String(c.options?.apiVersion ?? "7.1"),
    frontendUrl: String(c.options?.frontendUrl ?? ""),
    deployment: c.deployment,
    email: String(c.options?.email ?? ""),
    scoped: c.options?.scoped === true,
  });
  const [scope, setScope] = useState<Scope>(c.scope as Scope);
  const [globs, setGlobs] = useState<Globs>({
    include: (c.includeGlobs ?? []).join("\n"),
    exclude: (c.excludeGlobs ?? []).join("\n"),
    code: (c.codeGlobs ?? []).join("\n"),
  });
  const [schedule, setSchedule] = useState<Schedule>({
    syncMinutes: c.syncMinutes,
    reconcileHours: c.reconcileHours,
    rateConcurrency: c.rateConcurrency,
    ratePerSecond: c.ratePerSecond,
    parallelParts: c.parallelParts,
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
            ...sourceBody(source, scope, c.id, globs),
            connectionId: undefined,
            kind,
            name: source.name.trim(),
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
    if (needsScope(kind) && scopeList(kind, scope).length === 0) {
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
            onClick={() => test.mutate(sourceBody(source, scope, c.id, globs))}
          >
            {test.isPending ? t("connections.testing") : t("connections.test")}
          </Button>
          {test.isSuccess && (
            <Notice tone="success">
              {t(testCopy(kind, test.data.more ? "connections.testScopeOkMore" : "connections.testScopeOk"), {
                user: test.data.user,
                count: test.data.sample,
              })}
            </Notice>
          )}
          {test.isError && <Notice tone="danger">{messageOf(test.error)}</Notice>}
        </div>
        {kind === "web" ? (
          <WebScopeFields value={scope} onChange={setScope} />
        ) : (
          <ScopePicker source={source} connectionId={c.id} scope={scope} onChange={setScope} />
        )}
        {isRepo(kind) && <GlobFields value={globs} onChange={setGlobs} />}
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
                    {r.rewrite && (
                      <p className="mt-1" data-testid="run-rewrite">
                        {t("connections.rewrite", {
                          from: r.rewrite.from,
                          to: r.rewrite.to,
                          items: r.rewrite.items,
                          entities: r.rewrite.entities,
                          people: r.rewrite.people,
                          unmatched: r.rewrite.unmatched,
                        })}
                      </p>
                    )}
                    {r.parts.length > 1 && (
                      <details className="mt-1" data-testid="run-parts">
                        <summary className="cursor-pointer">
                          {t("connections.parts", { count: r.parts.length })}
                        </summary>
                        <ul>
                          {r.parts.map((p) => (
                            <li key={p.part}>
                              {t("connections.part", {
                                part: p.part,
                                status: t(`connections.run.${p.status}` as CopyKey),
                                host: p.host || t("connections.partNoHost"),
                              })}
                              {p.error && <span className="text-danger"> {p.error}</span>}
                            </li>
                          ))}
                        </ul>
                      </details>
                    )}
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

const usd = new Intl.NumberFormat(undefined, { maximumFractionDigits: 4 });

/** The admin extraction trigger: the estimate first, then the start. */
function ExtractionSheet({ c }: { c: Connection }) {
  const estimate = useMutation({
    mutationFn: async () =>
      unwrap(await api.GET("/api/v1/connections/{id}/extraction", { params: { path: { id: c.id } } })),
  });
  const start = useMutation({
    mutationFn: async (passages: number) =>
      unwrap(
        await api.POST("/api/v1/connections/{id}/extraction", {
          params: { path: { id: c.id } },
          body: { passages },
        }),
      ),
  });
  const stop = useMutation({
    mutationFn: async () =>
      unwrap(await api.DELETE("/api/v1/connections/{id}/extraction", { params: { path: { id: c.id } } })),
    onSuccess: () => {
      start.reset();
      estimate.reset();
    },
  });
  const e = estimate.data;
  const running = start.isSuccess || (e?.running ?? false);
  return (
    <Sheet title={t("extraction.title")} description={t("extraction.body")}>
      <div className="flex flex-col gap-3">
        <div className="flex flex-wrap gap-3">
          <Button
            disabled={estimate.isPending}
            onClick={() => {
              start.reset();
              stop.reset();
              estimate.mutate();
            }}
          >
            {estimate.isPending ? t("extraction.estimating") : t("extraction.estimate")}
          </Button>
          {e?.on && e.passages > 0 && !running && (
            <Button variant="primary" disabled={start.isPending} onClick={() => start.mutate(e.passages)}>
              {t("extraction.start", { passages: e.passages })}
            </Button>
          )}
          {running && (
            <Button variant="danger" disabled={stop.isPending} onClick={() => stop.mutate()}>
              {t("extraction.stop")}
            </Button>
          )}
        </div>
        {estimate.isError && <Notice tone="danger">{messageOf(estimate.error)}</Notice>}
        {e && e.passages === 0 && <Notice>{t("extraction.none")}</Notice>}
        {e && e.passages > 0 && (
          <Notice>
            {t("extraction.result", {
              passages: e.passages,
              tokens: e.tokensIn + e.tokensOut,
              cost: usd.format(e.costUsd),
            })}
          </Notice>
        )}
        {e && !e.on && <Notice tone="danger">{t("extraction.off")}</Notice>}
        {start.isSuccess && (
          <Notice tone="success">{t("extraction.queued", { count: start.data.queued })}</Notice>
        )}
        {!start.isSuccess && e?.running && <Notice>{t("extraction.running")}</Notice>}
        {stop.isSuccess && <Notice>{t("extraction.stopped")}</Notice>}
        {stop.isError && <Notice tone="danger">{messageOf(stop.error)}</Notice>}
        {start.isError && (
          <Notice tone="danger">
            {start.error instanceof ApiError && start.error.code === "estimate_changed"
              ? t("extraction.changed")
              : messageOf(start.error)}
          </Notice>
        )}
      </div>
    </Sheet>
  );
}
