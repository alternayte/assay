import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, Outlet } from "@tanstack/react-router";
import { type FormEvent, useEffect, useState } from "react";
import { ApiError, api, type Schemas, unwrap } from "@/api/client";
import { Button } from "@/components/ui/button";
import { Field, Input, Select, Textarea } from "@/components/ui/field";
import { Notice } from "@/components/ui/notice";
import { Empty, Sheet } from "@/components/ui/sheet";
import { SkeletonRows } from "@/components/ui/skeleton";
import { Table, Td, Th } from "@/components/ui/table";
import { type CopyKey, t } from "@/copy";
import { formatTime } from "@/lib/format";
import { navItems } from "@/lib/nav";

export function SettingsLayout() {
  const sections = navItems.filter((i) => i.group === "settings");
  return (
    <div className="flex flex-col gap-6">
      <h1 className="text-3xl font-semibold tracking-tight">{t("settings.title")}</h1>
      <div className="flex flex-col gap-6 md:flex-row">
        <nav aria-label={t("settings.sections")} className="md:w-48 md:shrink-0">
          <ul className="flex gap-1 overflow-x-auto md:flex-col">
            {sections.map((s) => (
              <li key={s.to}>
                <Link
                  to={s.to}
                  className="block rounded-sm px-2 py-1 text-base whitespace-nowrap text-ink-muted transition-ink hover:bg-paper-sunk hover:text-ink data-[status=active]:bg-accent-wash data-[status=active]:font-medium data-[status=active]:text-ink"
                >
                  {t(s.label)}
                </Link>
              </li>
            ))}
          </ul>
        </nav>
        <div className="min-w-0 flex-1">
          <Outlet />
        </div>
      </div>
    </div>
  );
}

type Setting = Schemas["Setting"];

export function useSetting(key: string) {
  return useQuery({
    queryKey: ["settings"],
    queryFn: async () => unwrap(await api.GET("/api/v1/settings")).settings,
    select: (list) => list.find((s) => s.key === key),
  });
}

export function useSaveSetting(key: string, onSaved: (s: Setting) => void, onError: (msg: string) => void) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (v: { value: unknown; revision: number }) =>
      unwrap(await api.PUT("/api/v1/settings/{key}", { params: { path: { key } }, body: v })),
    onSuccess: (s) => {
      qc.invalidateQueries({ queryKey: ["settings"] });
      onSaved(s);
    },
    onError: (e) =>
      onError(
        e instanceof ApiError
          ? e.code === "stale_revision"
            ? t("general.stale")
            : e.message
          : t("errors.network"),
      ),
  });
}

type Retention = { questionsDays: number; syncRunsDays: number; modelUsageDays: number; auditDays: number };
const retentionFields: { key: keyof Retention; label: Parameters<typeof t>[0] }[] = [
  { key: "questionsDays", label: "general.questionsDays" },
  { key: "syncRunsDays", label: "general.syncRunsDays" },
  { key: "modelUsageDays", label: "general.modelUsageDays" },
  { key: "auditDays", label: "general.auditDays" },
];

export function GeneralPage() {
  const setting = useSetting("retention");
  const [value, setValue] = useState<Retention>();
  const [msg, setMsg] = useState<{ tone: "success" | "danger"; text: string }>();
  useEffect(() => {
    if (setting.data) setValue(setting.data.value as Retention);
  }, [setting.data]);
  const save = useSaveSetting(
    "retention",
    (s) => setMsg({ tone: "success", text: t("general.saved", { revision: s.revision }) }),
    (text) => setMsg({ tone: "danger", text }),
  );

  if (setting.isPending || !value) return <SkeletonRows label={t("app.loading")} />;
  if (setting.isError || !setting.data) return <Notice tone="danger">{t("app.error")}</Notice>;
  const revision = setting.data.revision;

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setMsg(undefined);
    save.mutate({ value, revision });
  };
  return (
    <div className="flex flex-col gap-6">
      <Sheet title={t("general.retentionTitle")} description={t("general.retentionBody")}>
        <form onSubmit={submit} className="flex flex-col gap-4">
          {msg && <Notice tone={msg.tone}>{msg.text}</Notice>}
          <div className="grid gap-4 sm:grid-cols-2">
            {retentionFields.map((f) => (
              <Field key={f.key} label={t(f.label)}>
                {(p) => (
                  <Input
                    {...p}
                    type="number"
                    min={1}
                    required
                    className="font-mono"
                    value={value[f.key]}
                    onChange={(e) => setValue({ ...value, [f.key]: Number(e.target.value) })}
                  />
                )}
              </Field>
            ))}
          </div>
          <div>
            <Button type="submit" variant="primary" disabled={save.isPending}>
              {t("general.save")}
            </Button>
          </div>
        </form>
      </Sheet>
      <History settingKey="retention" />
      <PrioritySheet />
      <History settingKey="priority" />
      <PagesSheet />
      <History settingKey="pages" />
      <ConnectorRulesSheet />
      <History settingKey="connectors" />
    </div>
  );
}

type PagesValue = {
  sentenceWords: number;
  summaryWords: number;
  extractPassages: number;
  bannedWords: string[];
};
const pagesFields: {
  key: "sentenceWords" | "summaryWords" | "extractPassages";
  label: Parameters<typeof t>[0];
  min: number;
}[] = [
  { key: "sentenceWords", label: "general.sentenceWords", min: 5 },
  { key: "summaryWords", label: "general.summaryWords", min: 20 },
  { key: "extractPassages", label: "general.extractPassages", min: 0 },
];

/** The page rules: lint thresholds, banned words and the extraction limit. */
function PagesSheet() {
  const setting = useSetting("pages");
  const [value, setValue] = useState<PagesValue>();
  const [banned, setBanned] = useState("");
  const [msg, setMsg] = useState<{ tone: "success" | "danger"; text: string }>();
  useEffect(() => {
    if (!setting.data) return;
    const v = setting.data.value as PagesValue;
    setValue(v);
    setBanned(v.bannedWords.join("\n"));
  }, [setting.data]);
  const save = useSaveSetting(
    "pages",
    (s) => setMsg({ tone: "success", text: t("general.saved", { revision: s.revision }) }),
    (text) => setMsg({ tone: "danger", text }),
  );
  if (setting.isPending || !value) return <SkeletonRows label={t("app.loading")} />;
  if (setting.isError || !setting.data) return <Notice tone="danger">{t("app.error")}</Notice>;
  const revision = setting.data.revision;
  const submit = (e: FormEvent) => {
    e.preventDefault();
    setMsg(undefined);
    const bannedWords = banned
      .split("\n")
      .map((w) => w.trim())
      .filter(Boolean);
    save.mutate({ value: { ...value, bannedWords }, revision });
  };
  return (
    <Sheet title={t("general.pagesTitle")} description={t("general.pagesBody")}>
      <form onSubmit={submit} className="flex flex-col gap-4">
        {msg && <Notice tone={msg.tone}>{msg.text}</Notice>}
        <div className="grid gap-4 sm:grid-cols-3">
          {pagesFields.map((f) => (
            <Field key={f.key} label={t(f.label)}>
              {(p) => (
                <Input
                  {...p}
                  type="number"
                  min={f.min}
                  required
                  className="font-mono"
                  value={value[f.key]}
                  onChange={(e) => setValue({ ...value, [f.key]: Number(e.target.value) })}
                />
              )}
            </Field>
          ))}
        </div>
        <Field label={t("general.bannedWords")} hint={t("general.bannedWordsHint")}>
          {(p) => <Textarea {...p} rows={6} value={banned} onChange={(e) => setBanned(e.target.value)} />}
        </Field>
        <div>
          <Button type="submit" variant="primary" disabled={save.isPending}>
            {t("general.save")}
          </Button>
        </div>
      </form>
    </Sheet>
  );
}

type RulesValue = {
  initiativeTypes: string[];
  blockLinkType: string;
  decisionLabels: string[];
  glossaryLabels: string[];
  defaultGlobs: string[];
  maxFileKb: number;
  maxFilesPerRepo: number;
};
type RulesList = "initiativeTypes" | "decisionLabels" | "glossaryLabels" | "defaultGlobs";

const rulesLists: { key: RulesList; label: CopyKey; hint: CopyKey }[] = [
  { key: "initiativeTypes", label: "general.initiativeTypes", hint: "general.initiativeTypesHint" },
  { key: "decisionLabels", label: "general.decisionLabels", hint: "general.decisionLabelsHint" },
  { key: "glossaryLabels", label: "general.glossaryLabels", hint: "general.glossaryLabelsHint" },
  { key: "defaultGlobs", label: "general.defaultGlobs", hint: "general.defaultGlobsHint" },
];

const lines = (s: string) =>
  s
    .split("\n")
    .map((w) => w.trim())
    .filter(Boolean);

/** The connector rules: Jira and Confluence rules, globs and limits (REQ-SYN-011). */
function ConnectorRulesSheet() {
  const setting = useSetting("connectors");
  const [value, setValue] = useState<RulesValue>();
  const [text, setText] = useState<Record<RulesList, string>>();
  const [msg, setMsg] = useState<{ tone: "success" | "danger"; text: string }>();
  useEffect(() => {
    if (!setting.data) return;
    const v = setting.data.value as RulesValue;
    setValue(v);
    setText({
      initiativeTypes: v.initiativeTypes.join("\n"),
      decisionLabels: v.decisionLabels.join("\n"),
      glossaryLabels: v.glossaryLabels.join("\n"),
      defaultGlobs: v.defaultGlobs.join("\n"),
    });
  }, [setting.data]);
  const save = useSaveSetting(
    "connectors",
    (s) => setMsg({ tone: "success", text: t("general.saved", { revision: s.revision }) }),
    (m) => setMsg({ tone: "danger", text: m }),
  );
  if (setting.isPending || !value || !text) return <SkeletonRows label={t("app.loading")} />;
  if (setting.isError || !setting.data) return <Notice tone="danger">{t("app.error")}</Notice>;
  const revision = setting.data.revision;
  const submit = (e: FormEvent) => {
    e.preventDefault();
    setMsg(undefined);
    save.mutate({
      value: {
        ...value,
        initiativeTypes: lines(text.initiativeTypes),
        decisionLabels: lines(text.decisionLabels),
        glossaryLabels: lines(text.glossaryLabels),
        defaultGlobs: lines(text.defaultGlobs),
      },
      revision,
    });
  };
  return (
    <Sheet title={t("general.connectorsTitle")} description={t("general.connectorsBody")}>
      <form onSubmit={submit} className="flex flex-col gap-4">
        {msg && <Notice tone={msg.tone}>{msg.text}</Notice>}
        <div className="grid gap-4 sm:grid-cols-3">
          <Field label={t("general.blockLinkType")} hint={t("general.blockLinkTypeHint")}>
            {(p) => (
              <Input
                {...p}
                required
                value={value.blockLinkType}
                onChange={(e) => setValue({ ...value, blockLinkType: e.target.value })}
              />
            )}
          </Field>
          <Field label={t("general.maxFileKb")}>
            {(p) => (
              <Input
                {...p}
                type="number"
                min={1}
                required
                className="font-mono"
                value={value.maxFileKb}
                onChange={(e) => setValue({ ...value, maxFileKb: Number(e.target.value) })}
              />
            )}
          </Field>
          <Field label={t("general.maxFilesPerRepo")}>
            {(p) => (
              <Input
                {...p}
                type="number"
                min={1}
                required
                className="font-mono"
                value={value.maxFilesPerRepo}
                onChange={(e) => setValue({ ...value, maxFilesPerRepo: Number(e.target.value) })}
              />
            )}
          </Field>
        </div>
        <div className="grid gap-4 sm:grid-cols-2">
          {rulesLists.map((f) => (
            <Field key={f.key} label={t(f.label)} hint={t(f.hint)}>
              {(p) => (
                <Textarea
                  {...p}
                  rows={f.key === "defaultGlobs" ? 8 : 3}
                  className="font-mono text-sm"
                  value={text[f.key]}
                  onChange={(e) => setText({ ...text, [f.key]: e.target.value })}
                />
              )}
            </Field>
          ))}
        </div>
        <div>
          <Button type="submit" variant="primary" disabled={save.isPending}>
            {t("general.save")}
          </Button>
        </div>
      </form>
    </Sheet>
  );
}

type PriorityValue = { predicates: Record<string, string[]> };

/** The source priority per predicate (REQ-CLM-004). */
function PrioritySheet() {
  const setting = useSetting("priority");
  const registry = useQuery({
    queryKey: ["predicates"],
    queryFn: async () => unwrap(await api.GET("/api/v1/predicates")),
    staleTime: Number.POSITIVE_INFINITY,
  });
  const [orders, setOrders] = useState<Record<string, string>>();
  const [msg, setMsg] = useState<{ tone: "success" | "danger"; text: string }>();
  useEffect(() => {
    if (!setting.data) return;
    const v = setting.data.value as PriorityValue;
    const out: Record<string, string> = {};
    for (const [k, order] of Object.entries(v.predicates ?? {})) out[k] = order.join(", ");
    setOrders(out);
  }, [setting.data]);
  const save = useSaveSetting(
    "priority",
    (s) => setMsg({ tone: "success", text: t("priority.saved", { revision: s.revision }) }),
    (text) => setMsg({ tone: "danger", text }),
  );
  if (setting.isPending || registry.isPending || !orders) return <SkeletonRows label={t("app.loading")} />;
  if (setting.isError || registry.isError || !setting.data)
    return <Notice tone="danger">{t("app.error")}</Notice>;
  const revision = setting.data.revision;
  const submit = (e: FormEvent) => {
    e.preventDefault();
    setMsg(undefined);
    const predicates: Record<string, string[]> = {};
    for (const [k, v] of Object.entries(orders)) {
      const list = v
        .split(",")
        .map((x) => x.trim())
        .filter(Boolean);
      if (list.length > 0) predicates[k] = list;
    }
    save.mutate({ value: { predicates }, revision });
  };
  return (
    <Sheet title={t("priority.title")} description={t("priority.body")}>
      <form onSubmit={submit} className="flex flex-col gap-4">
        {msg && <Notice tone={msg.tone}>{msg.text}</Notice>}
        <p className="text-sm text-ink-muted">
          {t("priority.types", { types: registry.data.sourceTypes.join(", ") })}
        </p>
        <Table>
          <thead>
            <tr>
              <Th>{t("priority.colPredicate")}</Th>
              <Th>{t("priority.colOrder")}</Th>
            </tr>
          </thead>
          <tbody>
            {registry.data.predicates
              .filter((p) => !p.multi)
              .map((p) => (
                <tr key={p.name}>
                  <Td className="font-mono text-sm whitespace-nowrap">{p.name}</Td>
                  <Td>
                    <Input
                      aria-label={`${t("priority.colOrder")}: ${p.name}`}
                      className="font-mono text-sm"
                      placeholder={p.defaultPriority.join(", ")}
                      value={orders[p.name] ?? ""}
                      onChange={(e) => setOrders({ ...orders, [p.name]: e.target.value })}
                    />
                  </Td>
                </tr>
              ))}
          </tbody>
        </Table>
        <div>
          <Button type="submit" variant="primary" disabled={save.isPending}>
            {t("priority.save")}
          </Button>
        </div>
      </form>
    </Sheet>
  );
}

function History({ settingKey }: { settingKey: string }) {
  const history = useQuery({
    queryKey: ["settings", settingKey, "history"],
    queryFn: async () =>
      unwrap(await api.GET("/api/v1/settings/{key}/history", { params: { path: { key: settingKey } } }))
        .changes,
  });
  return (
    <Sheet title={t("general.history")}>
      {history.isPending ? (
        <SkeletonRows label={t("app.loading")} rows={2} />
      ) : history.isError ? (
        <Notice tone="danger">{t("app.error")}</Notice>
      ) : (
        <ol className="flex flex-col">
          {history.data.map((c) => (
            <li key={c.revision} className="border-b border-rule py-2 last:border-b-0">
              <p className="text-sm">
                <span className="font-mono">{t("general.revision", { revision: c.revision })}</span>
                <span className="text-ink-muted">
                  {" "}
                  · {formatTime(c.updatedAt)} ·{" "}
                  {c.updatedBy ? t("general.by", { email: c.updatedBy }) : t("general.system")}
                </span>
              </p>
              <pre className="mt-1 rounded-sm bg-paper-sunk px-2 py-1 font-mono text-xs break-all whitespace-pre-wrap">
                {JSON.stringify(c.newValue)}
              </pre>
            </li>
          ))}
        </ol>
      )}
    </Sheet>
  );
}

type SMTP = {
  host: string;
  port: number;
  username: string;
  from: string;
  security: "starttls" | "tls" | "none";
};

export function EmailPage() {
  const setting = useSetting("smtp");
  const [value, setValue] = useState<SMTP>();
  const [password, setPassword] = useState("");
  const [to, setTo] = useState("");
  const [msg, setMsg] = useState<{ tone: "success" | "danger"; text: string }>();
  useEffect(() => {
    if (setting.data) setValue(setting.data.value as SMTP);
  }, [setting.data]);
  const fail = (text: string) => setMsg({ tone: "danger", text });
  const save = useSaveSetting("smtp", () => setMsg({ tone: "success", text: t("smtp.saved") }), fail);
  const savePassword = useMutation({
    mutationFn: async (pw: string) =>
      unwrap(await api.PUT("/api/v1/settings/smtp/password", { body: { password: pw } })),
  });
  const test = useMutation({
    mutationFn: async () => unwrap(await api.POST("/api/v1/settings/smtp/test", { body: { to } })),
    onSuccess: () => setMsg({ tone: "success", text: t("smtp.testSent") }),
    onError: (e) =>
      fail(e instanceof ApiError ? (e.code === "smtp_off" ? t("smtp.off") : e.message) : t("errors.network")),
  });

  if (setting.isPending || !value) return <SkeletonRows label={t("app.loading")} />;
  if (setting.isError || !setting.data) return <Notice tone="danger">{t("app.error")}</Notice>;
  const revision = setting.data.revision;
  const set = (k: keyof SMTP) => (e: React.ChangeEvent<HTMLInputElement | HTMLSelectElement>) =>
    setValue({ ...value, [k]: k === "port" ? Number(e.target.value) : e.target.value });

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setMsg(undefined);
    try {
      if (password) {
        await savePassword.mutateAsync(password);
        setPassword("");
      }
      save.mutate({ value, revision });
    } catch {
      fail(t("errors.generic"));
    }
  };

  return (
    <div className="flex flex-col gap-6">
      <Sheet title={t("smtp.title")} description={t("smtp.body")}>
        <form onSubmit={submit} className="flex flex-col gap-4">
          {msg && <Notice tone={msg.tone}>{msg.text}</Notice>}
          <div className="grid gap-4 sm:grid-cols-[1fr_8rem]">
            <Field label={t("smtp.host")} hint={t("smtp.hostHint")}>
              {(p) => <Input {...p} className="font-mono" value={value.host} onChange={set("host")} />}
            </Field>
            <Field label={t("smtp.port")}>
              {(p) => (
                <Input
                  {...p}
                  type="number"
                  min={1}
                  max={65535}
                  className="font-mono"
                  value={value.port}
                  onChange={set("port")}
                />
              )}
            </Field>
          </div>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={t("smtp.security")}>
              {(p) => (
                <Select {...p} value={value.security} onChange={set("security")}>
                  <option value="starttls">{t("smtp.securityStarttls")}</option>
                  <option value="tls">{t("smtp.securityTls")}</option>
                  <option value="none">{t("smtp.securityNone")}</option>
                </Select>
              )}
            </Field>
            <Field label={t("smtp.from")}>
              {(p) => <Input {...p} type="email" value={value.from} onChange={set("from")} />}
            </Field>
            <Field label={t("smtp.username")}>
              {(p) => <Input {...p} autoComplete="off" value={value.username} onChange={set("username")} />}
            </Field>
            <Field label={t("smtp.password")} hint={t("smtp.passwordHint")}>
              {(p) => (
                <Input
                  {...p}
                  type="password"
                  autoComplete="new-password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                />
              )}
            </Field>
          </div>
          <div className="flex flex-wrap gap-2">
            <Button type="submit" variant="primary" disabled={save.isPending}>
              {t("smtp.save")}
            </Button>
            <Button
              onClick={() =>
                savePassword.mutate("", {
                  onSuccess: () => setMsg({ tone: "success", text: t("smtp.passwordRemoved") }),
                })
              }
            >
              {t("smtp.removePassword")}
            </Button>
          </div>
        </form>
      </Sheet>
      <Sheet title={t("smtp.testTitle")}>
        <form
          className="flex flex-wrap items-end gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            setMsg(undefined);
            test.mutate();
          }}
        >
          <Field label={t("smtp.testTo")} className="min-w-64 flex-1">
            {(p) => <Input {...p} type="email" required value={to} onChange={(e) => setTo(e.target.value)} />}
          </Field>
          <Button type="submit" disabled={test.isPending}>
            {t("smtp.test")}
          </Button>
        </form>
      </Sheet>
    </div>
  );
}

export function AuditPage() {
  const [draft, setDraft] = useState("");
  const [action, setAction] = useState("");
  const entries = useInfiniteQuery({
    queryKey: ["audit", action],
    initialPageParam: 0,
    queryFn: async ({ pageParam }) =>
      unwrap(
        await api.GET("/api/v1/audit", {
          params: { query: { action: action || undefined, before: pageParam || undefined, limit: 50 } },
        }),
      ),
    getNextPageParam: (last) => last.next || undefined,
  });
  const rows = entries.data?.pages.flatMap((p) => p.entries) ?? [];

  return (
    <Sheet title={t("audit.title")} description={t("audit.body")}>
      <form
        className="mb-4 flex flex-wrap items-end gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          setAction(draft.trim());
        }}
      >
        <Field label={t("audit.filter")} hint={t("audit.filterHint")} className="min-w-64 flex-1">
          {(p) => (
            <Input {...p} className="font-mono" value={draft} onChange={(e) => setDraft(e.target.value)} />
          )}
        </Field>
        <Button type="submit">{t("audit.apply")}</Button>
      </form>
      {entries.isPending ? (
        <SkeletonRows label={t("app.loading")} />
      ) : entries.isError ? (
        <Notice tone="danger">{t("app.error")}</Notice>
      ) : rows.length === 0 ? (
        <Empty>{t("audit.empty")}</Empty>
      ) : (
        <>
          <Table>
            <thead>
              <tr>
                <Th>{t("audit.colTime")}</Th>
                <Th>{t("audit.colActor")}</Th>
                <Th>{t("audit.colAction")}</Th>
                <Th>{t("audit.colTarget")}</Th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => (
                <tr key={r.id}>
                  <Td className="whitespace-nowrap">{formatTime(r.at)}</Td>
                  <Td className="font-mono text-sm">{r.actorEmail || t("audit.system")}</Td>
                  <Td className="font-mono text-sm">{r.action}</Td>
                  <Td className="font-mono text-sm text-ink-muted">
                    {r.hidden ? t("audit.hidden") : r.target}
                  </Td>
                </tr>
              ))}
            </tbody>
          </Table>
          {entries.hasNextPage && (
            <Button
              className="mt-3"
              onClick={() => entries.fetchNextPage()}
              disabled={entries.isFetchingNextPage}
            >
              {t("audit.more")}
            </Button>
          )}
        </>
      )}
    </Sheet>
  );
}
