import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Plus } from "lucide-react";
import { type FormEvent, useState } from "react";
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

type Endpoint = Schemas["EndpointRecord"];
type Fn = Schemas["FunctionRecord"];

const fnHelp: Record<Fn["function"], CopyKey> = {
  answer: "models.fnAnswer",
  extract: "models.fnExtract",
  compile: "models.fnCompile",
  embed: "models.fnEmbed",
  rerank: "models.fnRerank",
  decide: "models.fnDecide",
};

function messageOf(e: unknown): string {
  return e instanceof ApiError ? e.message : t("errors.network");
}

const usd = new Intl.NumberFormat(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 4 });

export function ModelsPage() {
  const endpoints = useQuery({
    queryKey: ["models", "endpoints"],
    queryFn: async () => unwrap(await api.GET("/api/v1/models/endpoints")).endpoints,
  });
  const functions = useQuery({
    queryKey: ["models", "functions"],
    queryFn: async () => unwrap(await api.GET("/api/v1/models/functions")).functions,
  });
  const [editing, setEditing] = useState<Endpoint | "new">();
  const [editFn, setEditFn] = useState<Fn>();

  return (
    <div className="flex flex-col gap-6">
      <Sheet
        title={t("models.endpointsTitle")}
        description={t("models.endpointsBody")}
        actions={
          <Button variant="primary" onClick={() => setEditing("new")}>
            <Plus aria-hidden />
            {t("models.addEndpoint")}
          </Button>
        }
      >
        {endpoints.isPending ? (
          <SkeletonRows label={t("app.loading")} rows={2} />
        ) : endpoints.isError ? (
          <Notice tone="danger">{t("app.error")}</Notice>
        ) : endpoints.data.length === 0 ? (
          <Empty
            action={
              <Button variant="primary" onClick={() => setEditing("new")}>
                {t("models.addEndpoint")}
              </Button>
            }
          >
            {t("models.noEndpoints")}
          </Empty>
        ) : (
          <ul className="flex flex-col">
            {endpoints.data.map((e) => (
              <EndpointRow key={e.id} endpoint={e} onEdit={() => setEditing(e)} />
            ))}
          </ul>
        )}
      </Sheet>

      <Sheet title={t("models.functionsTitle")} description={t("models.functionsBody")}>
        {functions.isPending ? (
          <SkeletonRows label={t("app.loading")} rows={6} />
        ) : functions.isError ? (
          <Notice tone="danger">{t("app.error")}</Notice>
        ) : (
          <Table>
            <thead>
              <tr>
                <Th>{t("models.colFunction")}</Th>
                <Th>{t("models.colModel")}</Th>
                <Th>{t("models.colSpend")}</Th>
                <Th>{t("models.colStatus")}</Th>
                <Th>
                  <span className="sr-only">{t("users.colActions")}</span>
                </Th>
              </tr>
            </thead>
            <tbody>
              {functions.data.map((f) => (
                <tr key={f.function}>
                  <Td>
                    <div className="font-mono font-medium">{f.function}</div>
                    <div className="text-sm text-ink-muted">{t(fnHelp[f.function])}</div>
                  </Td>
                  <Td>
                    <div className="font-mono text-sm">{f.model}</div>
                    <div className="text-sm text-ink-muted">{f.endpointName || t("models.noEndpoint")}</div>
                  </Td>
                  <Td className="whitespace-nowrap">
                    <div className="font-mono text-sm">
                      {t("models.spend", {
                        spent: usd.format(f.spentUsd),
                        budget: usd.format(f.monthlyBudgetUsd),
                      })}
                    </div>
                    <div className="text-sm text-ink-muted">{t("models.calls", { count: f.calls })}</div>
                  </Td>
                  <Td>
                    {f.overBudget ? (
                      <Badge tone="danger">{t("models.overBudget")}</Badge>
                    ) : f.enabled ? (
                      <Badge tone="supported">{t("models.on")}</Badge>
                    ) : (
                      <Badge>{t("models.off")}</Badge>
                    )}
                  </Td>
                  <Td className="text-right">
                    <Button size="sm" variant="ghost" onClick={() => setEditFn(f)}>
                      {t("models.edit")}
                    </Button>
                  </Td>
                </tr>
              ))}
            </tbody>
          </Table>
        )}
      </Sheet>

      {editing && (
        <EndpointDialog
          key={editing === "new" ? "new" : editing.id}
          endpoint={editing === "new" ? undefined : editing}
          onClose={() => setEditing(undefined)}
        />
      )}
      {editFn && (
        <FunctionDialog
          key={editFn.function}
          fn={editFn}
          endpoints={endpoints.data ?? []}
          onClose={() => setEditFn(undefined)}
        />
      )}
    </div>
  );
}

function EndpointRow({ endpoint, onEdit }: { endpoint: Endpoint; onEdit: () => void }) {
  const [model, setModel] = useState("");
  const [result, setResult] = useState<{ tone: "success" | "danger"; text: string }>();
  const test = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.POST("/api/v1/models/endpoints/{id}/test", {
          params: { path: { id: endpoint.id } },
          body: { model: model || undefined },
        }),
      ),
    onSuccess: (r) =>
      setResult(
        r.ok
          ? { tone: "success", text: t("models.testOk", { model: r.model, count: r.models.length }) }
          : { tone: "danger", text: t("models.testFailed", { message: r.message }) },
      ),
    onError: (e) => setResult({ tone: "danger", text: t("models.testFailed", { message: messageOf(e) }) }),
  });
  return (
    <li className="flex flex-col gap-3 border-b border-rule py-3 last:border-b-0">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <p className="font-medium">
            {endpoint.name}
            <Badge className="ml-2" tone="accent">
              {endpoint.kind === "decisions" ? t("models.kindDecisions") : t("models.kindOpenai")}
            </Badge>
            <Badge className="ml-2" tone={endpoint.hasKey ? "supported" : "neutral"}>
              {endpoint.hasKey ? t("models.keySet") : t("models.noKey")}
            </Badge>
          </p>
          <p className="font-mono text-xs break-all text-ink-muted">{endpoint.baseUrl}</p>
        </div>
        <Button size="sm" onClick={onEdit}>
          {t("models.edit")}
        </Button>
      </div>
      <form
        className="flex flex-wrap items-end gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          setResult(undefined);
          test.mutate();
        }}
      >
        <Field label={t("models.testModel")} hint={t("models.testModelHint")} className="min-w-56 flex-1">
          {(p) => (
            <Input {...p} className="font-mono" value={model} onChange={(e) => setModel(e.target.value)} />
          )}
        </Field>
        <Button type="submit" disabled={test.isPending}>
          {t("models.test")}
        </Button>
      </form>
      {result && <Notice tone={result.tone}>{result.text}</Notice>}
    </li>
  );
}

function headersToText(h?: Record<string, string>): string {
  return Object.entries(h ?? {})
    .map(([k, v]) => `${k}: ${v}`)
    .join("\n");
}

function textToHeaders(s: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const line of s.split("\n")) {
    const i = line.indexOf(":");
    if (i > 0) out[line.slice(0, i).trim()] = line.slice(i + 1).trim();
  }
  return out;
}

function EndpointDialog({ endpoint, onClose }: { endpoint?: Endpoint; onClose: () => void }) {
  const qc = useQueryClient();
  const [name, setName] = useState(endpoint?.name ?? "");
  const [kind, setKind] = useState<Endpoint["kind"]>(endpoint?.kind ?? "openai");
  const [baseUrl, setBaseUrl] = useState(endpoint?.baseUrl ?? "https://openrouter.ai/api/v1");
  const [apiKey, setApiKey] = useState("");
  const [clearKey, setClearKey] = useState(false);
  const [headers, setHeaders] = useState(headersToText(endpoint?.extra.headers));
  const [body, setBody] = useState(endpoint?.extra.body ? JSON.stringify(endpoint.extra.body, null, 2) : "");
  const [error, setError] = useState<string>();
  const [confirmDelete, setConfirmDelete] = useState(false);
  const refresh = () => qc.invalidateQueries({ queryKey: ["models"] });

  const save = useMutation({
    mutationFn: async () => {
      let extraBody: Record<string, unknown> | undefined;
      if (body.trim()) {
        const parsed: unknown = JSON.parse(body);
        if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed))
          throw new Error(t("models.invalidJson"));
        extraBody = parsed as Record<string, unknown>;
      }
      const payload = {
        name,
        kind,
        baseUrl,
        apiKey: apiKey || undefined,
        clearKey: clearKey || undefined,
        extra: { headers: textToHeaders(headers), body: extraBody },
      };
      if (endpoint) {
        unwrap(
          await api.PUT("/api/v1/models/endpoints/{id}", {
            params: { path: { id: endpoint.id } },
            body: payload,
          }),
        );
      } else {
        unwrap(await api.POST("/api/v1/models/endpoints", { body: payload }));
      }
    },
    onSuccess: () => {
      refresh();
      onClose();
    },
    onError: (e) =>
      setError(
        e instanceof SyntaxError ? t("models.invalidJson") : e instanceof Error ? e.message : messageOf(e),
      ),
  });
  const del = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.DELETE("/api/v1/models/endpoints/{id}", { params: { path: { id: endpoint?.id ?? 0 } } }),
      ),
    onSuccess: () => {
      refresh();
      onClose();
    },
    onError: (e) => setError(messageOf(e)),
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    setError(undefined);
    save.mutate();
  };

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent
        title={endpoint ? t("models.editEndpoint") : t("models.addEndpoint")}
        closeLabel={t("models.cancel")}
      >
        {confirmDelete ? (
          <div className="flex flex-col gap-4">
            <p>{t("models.confirmDelete", { name: endpoint?.name ?? "" })}</p>
            <div className="flex justify-end gap-2">
              <Button onClick={() => setConfirmDelete(false)}>{t("models.cancel")}</Button>
              <Button variant="danger" onClick={() => del.mutate()}>
                {t("models.delete")}
              </Button>
            </div>
          </div>
        ) : (
          <form onSubmit={submit} className="flex flex-col gap-4">
            {error && <Notice tone="danger">{error}</Notice>}
            <Field label={t("models.name")}>
              {(p) => (
                <Input
                  {...p}
                  required
                  maxLength={100}
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                />
              )}
            </Field>
            <Field label={t("models.kind")}>
              {(p) => (
                <Select
                  {...p}
                  value={kind}
                  onChange={(e) => {
                    const k = e.target.value as Endpoint["kind"];
                    setKind(k);
                    if (!endpoint)
                      setBaseUrl(
                        k === "decisions" ? "https://openrouter.ai/api" : "https://openrouter.ai/api/v1",
                      );
                  }}
                >
                  <option value="openai">{t("models.kindOpenai")}</option>
                  <option value="decisions">{t("models.kindDecisions")}</option>
                </Select>
              )}
            </Field>
            <Field
              label={t("models.baseUrl")}
              hint={kind === "decisions" ? t("models.baseUrlHintDecisions") : t("models.baseUrlHintOpenai")}
            >
              {(p) => (
                <Input
                  {...p}
                  required
                  type="url"
                  className="font-mono"
                  value={baseUrl}
                  onChange={(e) => setBaseUrl(e.target.value)}
                />
              )}
            </Field>
            <Field label={t("models.apiKey")} hint={t("models.apiKeyHint")}>
              {(p) => (
                <Input
                  {...p}
                  type="password"
                  autoComplete="off"
                  value={apiKey}
                  onChange={(e) => setApiKey(e.target.value)}
                />
              )}
            </Field>
            {endpoint?.hasKey && (
              <label className="flex items-center gap-2 text-base">
                <input type="checkbox" checked={clearKey} onChange={(e) => setClearKey(e.target.checked)} />
                {t("models.clearKey")}
              </label>
            )}
            <Field label={t("models.headers")} hint={t("models.headersHint")}>
              {(p) => (
                <textarea
                  {...p}
                  rows={3}
                  className="w-full rounded-sm border border-rule-strong bg-paper-raised px-3 py-2 font-mono text-sm text-ink"
                  value={headers}
                  onChange={(e) => setHeaders(e.target.value)}
                />
              )}
            </Field>
            <Field label={t("models.bodyDefaults")} hint={t("models.bodyDefaultsHint")}>
              {(p) => (
                <textarea
                  {...p}
                  rows={3}
                  className="w-full rounded-sm border border-rule-strong bg-paper-raised px-3 py-2 font-mono text-sm text-ink"
                  value={body}
                  onChange={(e) => setBody(e.target.value)}
                />
              )}
            </Field>
            <div className="flex justify-between gap-2">
              {endpoint ? (
                <Button variant="danger" onClick={() => setConfirmDelete(true)}>
                  {t("models.delete")}
                </Button>
              ) : (
                <span />
              )}
              <Button type="submit" variant="primary" disabled={save.isPending}>
                {t("models.save")}
              </Button>
            </div>
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
}

function FunctionDialog({ fn, endpoints, onClose }: { fn: Fn; endpoints: Endpoint[]; onClose: () => void }) {
  const qc = useQueryClient();
  const [endpointId, setEndpointId] = useState(fn.endpointId ?? 0);
  const [model, setModel] = useState(fn.model);
  const [priceIn, setPriceIn] = useState(fn.priceInPerMTok);
  const [priceOut, setPriceOut] = useState(fn.priceOutPerMTok);
  const [budget, setBudget] = useState(fn.monthlyBudgetUsd);
  const [params, setParams] = useState(JSON.stringify(fn.params));
  const [enabled, setEnabled] = useState(fn.enabled);
  const [error, setError] = useState<string>();
  const save = useMutation({
    mutationFn: async () => {
      const parsed: unknown = JSON.parse(params || "{}");
      if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) throw new SyntaxError();
      unwrap(
        await api.PUT("/api/v1/models/functions/{function}", {
          params: { path: { function: fn.function } },
          body: {
            endpointId: endpointId || null,
            model,
            params: parsed as Record<string, unknown>,
            priceInPerMTok: priceIn,
            priceOutPerMTok: priceOut,
            monthlyBudgetUsd: budget,
            enabled,
          },
        }),
      );
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["models"] });
      onClose();
    },
    onError: (e) => setError(e instanceof SyntaxError ? t("models.invalidParams") : messageOf(e)),
  });
  const num = (set: (n: number) => void) => (e: React.ChangeEvent<HTMLInputElement>) =>
    set(Number(e.target.value));
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent
        title={t("models.editFunction", { name: fn.function })}
        description={t(fnHelp[fn.function])}
        closeLabel={t("models.cancel")}
      >
        <form
          className="flex flex-col gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            setError(undefined);
            save.mutate();
          }}
        >
          {error && <Notice tone="danger">{error}</Notice>}
          <Field label={t("models.endpoint")}>
            {(p) => (
              <Select {...p} value={endpointId} onChange={(e) => setEndpointId(Number(e.target.value))}>
                <option value={0}>{t("models.noEndpoint")}</option>
                {endpoints.map((e) => (
                  <option key={e.id} value={e.id}>
                    {e.name}
                  </option>
                ))}
              </Select>
            )}
          </Field>
          <Field label={t("models.model")}>
            {(p) => (
              <Input {...p} className="font-mono" value={model} onChange={(e) => setModel(e.target.value)} />
            )}
          </Field>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={t("models.priceIn")}>
              {(p) => (
                <Input
                  {...p}
                  type="number"
                  min={0}
                  step="any"
                  className="font-mono"
                  value={priceIn}
                  onChange={num(setPriceIn)}
                />
              )}
            </Field>
            <Field label={t("models.priceOut")}>
              {(p) => (
                <Input
                  {...p}
                  type="number"
                  min={0}
                  step="any"
                  className="font-mono"
                  value={priceOut}
                  onChange={num(setPriceOut)}
                />
              )}
            </Field>
          </div>
          <Field label={t("models.budget")}>
            {(p) => (
              <Input
                {...p}
                type="number"
                min={0}
                step="any"
                className="font-mono"
                value={budget}
                onChange={num(setBudget)}
              />
            )}
          </Field>
          <Field label={t("models.params")} hint={t("models.paramsHint")}>
            {(p) => (
              <Input
                {...p}
                className="font-mono"
                value={params}
                onChange={(e) => setParams(e.target.value)}
              />
            )}
          </Field>
          <label className="flex items-center gap-2 text-base">
            <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
            {t("models.enabled")}
          </label>
          <div className="flex justify-end">
            <Button type="submit" variant="primary" disabled={save.isPending}>
              {t("models.save")}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
