import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Check, Copy, KeyRound, Plus } from "lucide-react";
import { type FormEvent, useState } from "react";
import { ApiError, api, type Schemas, unwrap } from "@/api/client";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent } from "@/components/ui/dialog";
import { Field, Input } from "@/components/ui/field";
import { Notice } from "@/components/ui/notice";
import { Empty, Sheet } from "@/components/ui/sheet";
import { SkeletonRows } from "@/components/ui/skeleton";
import { Table, Td, Th } from "@/components/ui/table";
import { t } from "@/copy";
import { formatAgo, formatTime } from "@/lib/format";

type Token = Schemas["Token"];

const errorText = (e: unknown) => (e instanceof ApiError ? e.message : t("errors.network"));

/** A button that copies text and says so for two seconds. */
function CopyButton({ text, label, done }: { text: string; label: string; done: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <Button
      size="sm"
      onClick={async () => {
        await navigator.clipboard.writeText(text);
        setCopied(true);
        setTimeout(() => setCopied(false), 2000);
      }}
    >
      {copied ? <Check aria-hidden /> : <Copy aria-hidden />}
      {copied ? done : label}
    </Button>
  );
}

function state(k: Token): { tone: "supported" | "neutral" | "stale"; text: string } {
  if (k.revokedAt) return { tone: "neutral", text: t("tokens.revoked") };
  if (k.expiresAt && new Date(k.expiresAt) < new Date()) return { tone: "stale", text: t("tokens.expired") };
  return { tone: "supported", text: t("tokens.active") };
}

const SCOPES = ["read", "propose"] as const;
type Scope = (typeof SCOPES)[number];

function CreateDialog({ onClose, onCreated }: { onClose: () => void; onCreated: (value: string) => void }) {
  const [name, setName] = useState("");
  const [days, setDays] = useState("90");
  const [scopes, setScopes] = useState<Scope[]>(["read"]);
  const toggle = (s: Scope, on: boolean) =>
    setScopes((cur) => SCOPES.filter((x) => (x === s ? on : cur.includes(x))));
  const create = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.POST("/api/v1/tokens", {
          body: { name: name.trim(), expiresInDays: Number(days) || undefined, scopes },
        }),
      ),
    onSuccess: (r) => onCreated(r.value),
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    create.mutate();
  };
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent title={t("tokens.create")} closeLabel={t("tokens.close")}>
        <form onSubmit={submit} className="flex flex-col gap-4">
          {create.isError && <Notice tone="danger">{errorText(create.error)}</Notice>}
          <Field label={t("tokens.name")} hint={t("tokens.nameHint")}>
            {(p) => (
              <Input {...p} required maxLength={100} value={name} onChange={(e) => setName(e.target.value)} />
            )}
          </Field>
          <Field label={t("tokens.days")} hint={t("tokens.daysHint")}>
            {(p) => (
              <Input
                {...p}
                type="number"
                min={1}
                max={365}
                value={days}
                onChange={(e) => setDays(e.target.value)}
              />
            )}
          </Field>
          <fieldset className="flex flex-col gap-2">
            <legend className="mb-1 font-medium">{t("tokens.scopes")}</legend>
            {SCOPES.map((s) => (
              <label key={s} className="flex items-start gap-2 text-base">
                <input
                  type="checkbox"
                  className="mt-1"
                  checked={scopes.includes(s)}
                  onChange={(e) => toggle(s, e.target.checked)}
                />
                <span className="flex flex-col">
                  <span>{t(`tokens.${s}`)}</span>
                  <span className="text-sm text-ink-muted">{t(`tokens.${s}Hint`)}</span>
                </span>
              </label>
            ))}
            <p className="text-sm text-ink-muted">{t("tokens.scopesHint")}</p>
          </fieldset>
          <div className="flex gap-3">
            <Button onClick={onClose}>{t("tokens.cancel")}</Button>
            <Button
              type="submit"
              variant="primary"
              disabled={!name.trim() || scopes.length === 0 || create.isPending}
            >
              {t("tokens.save")}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

/** Access tokens: create, list and revoke the tokens of the user (REQ-AGT-005). */
export function TokensPage() {
  const qc = useQueryClient();
  const list = useQuery({
    queryKey: ["tokens"],
    queryFn: async () => unwrap(await api.GET("/api/v1/tokens")).tokens,
  });
  const [creating, setCreating] = useState(false);
  const [value, setValue] = useState<string>();
  const [msg, setMsg] = useState<string>();
  const revoke = useMutation({
    mutationFn: async (id: string) =>
      unwrap(await api.DELETE("/api/v1/tokens/{id}", { params: { path: { id } } })),
    onSuccess: () => {
      setMsg(t("tokens.revokedDone"));
      qc.invalidateQueries({ queryKey: ["tokens"] });
    },
  });
  const create = (
    <Button variant="primary" onClick={() => setCreating(true)}>
      <Plus aria-hidden />
      {t("tokens.create")}
    </Button>
  );
  return (
    <div className="flex flex-col gap-6">
      <header className="flex flex-wrap items-end justify-between gap-4 border-b border-rule-strong pb-4">
        <div className="flex flex-col gap-1">
          <h1 className="text-3xl font-semibold tracking-tight">{t("tokens.title")}</h1>
          <p className="text-base text-ink-muted">{t("tokens.body")}</p>
        </div>
        {create}
      </header>
      {value && (
        <div
          className="flex flex-col gap-2 rounded-sm border-l-4 border-accent bg-accent-wash px-3 py-2"
          data-testid="token-value"
        >
          <p className="flex items-center gap-2 font-medium">
            <KeyRound className="size-4" aria-hidden />
            {t("tokens.value")}
          </p>
          <div className="flex flex-wrap items-center gap-2">
            <code className="min-w-0 flex-1 rounded-sm border border-rule bg-paper-raised px-2 py-1 font-mono text-sm break-all">
              {value}
            </code>
            <CopyButton text={value} label={t("tokens.copy")} done={t("tokens.copied")} />
          </div>
        </div>
      )}
      {msg && <Notice tone="success">{msg}</Notice>}
      {revoke.isError && <Notice tone="danger">{errorText(revoke.error)}</Notice>}
      {list.isPending ? (
        <SkeletonRows label={t("app.loading")} rows={3} />
      ) : list.isError ? (
        <Notice tone="danger">{errorText(list.error)}</Notice>
      ) : list.data.length === 0 ? (
        <Empty action={create}>{t("tokens.empty")}</Empty>
      ) : (
        <Table>
          <thead>
            <tr>
              <Th>{t("tokens.colName")}</Th>
              <Th>{t("tokens.colStart")}</Th>
              <Th>{t("tokens.colCreated")}</Th>
              <Th>{t("tokens.colExpires")}</Th>
              <Th>{t("tokens.colScopes")}</Th>
              <Th>{t("tokens.colUsed")}</Th>
              <Th>{t("tokens.colStatus")}</Th>
              <Th>
                <span className="sr-only">{t("tokens.revoke")}</span>
              </Th>
            </tr>
          </thead>
          <tbody>
            {list.data.map((k) => {
              const s = state(k);
              return (
                <tr key={k.id} data-testid="token">
                  <Td>{k.name}</Td>
                  <Td className="font-mono text-sm">{k.start}…</Td>
                  <Td className="font-mono text-sm">{formatTime(k.createdAt)}</Td>
                  <Td className="font-mono text-sm">{k.expiresAt ? formatTime(k.expiresAt) : "—"}</Td>
                  <Td data-testid="scopes">{k.scopes.map((s) => t(`tokens.${s}`)).join(", ")}</Td>
                  <Td className="font-mono text-sm" data-testid="last-used">
                    {k.lastUsedAt ? formatAgo(k.lastUsedAt) : t("tokens.never")}
                  </Td>
                  <Td>
                    <Badge tone={s.tone}>{s.text}</Badge>
                  </Td>
                  <Td>
                    {!k.revokedAt && (
                      <Button
                        size="sm"
                        variant="danger"
                        aria-label={t("tokens.revokeName", { name: k.name })}
                        disabled={revoke.isPending}
                        onClick={() => revoke.mutate(k.id)}
                      >
                        {t("tokens.revoke")}
                      </Button>
                    )}
                  </Td>
                </tr>
              );
            })}
          </tbody>
        </Table>
      )}
      {creating && (
        <CreateDialog
          onClose={() => setCreating(false)}
          onCreated={(v) => {
            setCreating(false);
            setMsg(undefined);
            setValue(v);
            qc.invalidateQueries({ queryKey: ["tokens"] });
          }}
        />
      )}
    </div>
  );
}

/** Connect an agent: a copyable MCP client configuration (REQ-AGT-007). */
export function ConnectPage() {
  const info = useQuery({
    queryKey: ["connect"],
    queryFn: async () => unwrap(await api.GET("/api/v1/connect")),
  });
  if (info.isPending) return <SkeletonRows label={t("app.loading")} rows={6} />;
  if (info.isError) return <Notice tone="danger">{errorText(info.error)}</Notice>;
  const c = info.data;
  const config = JSON.stringify(
    {
      mcpServers: {
        assay: {
          type: "http",
          url: c.mcpUrl,
          headers: { Authorization: `Bearer ${t("connect.tokenText")}` },
        },
      },
    },
    null,
    2,
  );
  const urls: [string, string][] = [
    [t("connect.mcp"), c.mcpUrl],
    [t("connect.glossary"), c.glossaryUrl],
    [t("connect.openapi"), c.openapiUrl],
    [t("connect.docs"), c.docsUrl],
  ];
  return (
    <div className="flex flex-col gap-6">
      <header className="flex flex-col gap-1 border-b border-rule-strong pb-4">
        <h1 className="text-3xl font-semibold tracking-tight">{t("connect.title")}</h1>
        <p className="text-base text-ink-muted">{t("connect.body")}</p>
      </header>
      <ol className="flex flex-col gap-2 pl-5 [list-style:decimal]">
        <li>
          {t("connect.step1")}{" "}
          <Link to="/tokens" className="text-accent underline underline-offset-2">
            {t("connect.step1Link")}
          </Link>
        </li>
        <li>{t("connect.step2")}</li>
      </ol>
      <Sheet
        title={t("connect.config")}
        actions={<CopyButton text={config} label={t("connect.copy")} done={t("connect.copied")} />}
      >
        <pre
          className="rounded-sm bg-paper-sunk px-3 py-2 font-mono text-sm whitespace-pre-wrap break-all"
          data-testid="mcp-config"
        >
          {config}
        </pre>
      </Sheet>
      <Sheet title={t("connect.urls")}>
        <dl className="grid gap-x-6 gap-y-2 sm:grid-cols-[max-content_1fr]">
          {urls.map(([label, u]) => (
            <div key={label} className="contents">
              <dt className="label pt-1">{label}</dt>
              <dd className="font-mono text-sm break-all">
                <a href={u} className="text-accent underline-offset-2 hover:underline">
                  {u}
                </a>
              </dd>
            </div>
          ))}
        </dl>
      </Sheet>
    </div>
  );
}
