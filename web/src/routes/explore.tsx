import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Merge, X } from "lucide-react";
import { useEffect, useState } from "react";
import { api, unwrap } from "@/api/client";
import { MergeDialog } from "@/components/merge";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Field, Input, Select } from "@/components/ui/field";
import { Notice } from "@/components/ui/notice";
import { Empty } from "@/components/ui/sheet";
import { SkeletonRows } from "@/components/ui/skeleton";
import { Table, Td, Th } from "@/components/ui/table";
import { type CopyKey, t } from "@/copy";
import { formatAgo } from "@/lib/format";
import { ThingDialog } from "@/routes/registry";

const typeLabel = (k: string) => t(`entityTypes.${k}` as CopyKey);

function useDebounced<T>(value: T, ms: number): T {
  const [v, setV] = useState(value);
  useEffect(() => {
    const id = setTimeout(() => setV(value), ms);
    return () => clearTimeout(id);
  }, [value, ms]);
  return v;
}

/** Explore: the visible entities with filters, search and multi-select. */
export function ExplorePage() {
  const qc = useQueryClient();
  const [type, setType] = useState("");
  const [domain, setDomain] = useState("");
  const [status, setStatus] = useState("");
  const [q, setQ] = useState("");
  const query = useDebounced(q.trim(), 250);
  const [selected, setSelected] = useState<string[]>([]);
  const [merging, setMerging] = useState(false);
  const [msg, setMsg] = useState<string>();

  const registry = useQuery({
    queryKey: ["predicates"],
    queryFn: async () => unwrap(await api.GET("/api/v1/predicates")),
    staleTime: Number.POSITIVE_INFINITY,
  });
  const domains = useQuery({
    queryKey: ["domains"],
    queryFn: async () => unwrap(await api.GET("/api/v1/domains")).domains,
  });
  const list = useQuery({
    queryKey: ["entities", type, domain, status, query],
    queryFn: async () =>
      unwrap(
        await api.GET("/api/v1/entities", {
          params: {
            query: {
              type: type || undefined,
              domain: domain ? Number(domain) : undefined,
              status: (status || undefined) as "active" | "proposed" | undefined,
              q: query || undefined,
              limit: 200,
            },
          },
        }),
      ).entities,
  });

  const rows = list.data ?? [];
  const allSelected = rows.length > 0 && rows.every((r) => selected.includes(r.key));
  const toggle = (key: string) =>
    setSelected((s) => (s.includes(key) ? s.filter((k) => k !== key) : [...s, key]));

  return (
    <div className="flex flex-col gap-6">
      <header className="flex flex-wrap items-end justify-between gap-4">
        <div className="flex flex-col gap-1">
          <h1 className="text-3xl font-semibold tracking-tight">{t("explore.title")}</h1>
          <p className="text-base text-ink-muted">{t("explore.body")}</p>
        </div>
        <ThingDialog />
      </header>
      <div className="grid gap-3 sm:grid-cols-[minmax(0,2fr)_repeat(3,minmax(0,1fr))]">
        <Field label={t("explore.search")}>
          {(p) => <Input {...p} type="search" value={q} onChange={(e) => setQ(e.target.value)} />}
        </Field>
        <Field label={t("explore.type")}>
          {(p) => (
            <Select {...p} value={type} onChange={(e) => setType(e.target.value)}>
              <option value="">{t("explore.all")}</option>
              {registry.data?.entityTypes.map((et) => (
                <option key={et} value={et}>
                  {typeLabel(et)}
                </option>
              ))}
            </Select>
          )}
        </Field>
        <Field label={t("explore.domain")}>
          {(p) => (
            <Select {...p} value={domain} onChange={(e) => setDomain(e.target.value)}>
              <option value="">{t("explore.all")}</option>
              {domains.data?.map((d) => (
                <option key={d.id} value={d.id}>
                  {d.name}
                </option>
              ))}
            </Select>
          )}
        </Field>
        <Field label={t("explore.status")}>
          {(p) => (
            <Select {...p} value={status} onChange={(e) => setStatus(e.target.value)}>
              <option value="">{t("explore.all")}</option>
              <option value="active">{t("explore.active")}</option>
              <option value="proposed">{t("explore.proposed")}</option>
            </Select>
          )}
        </Field>
      </div>

      {msg && <Notice tone="success">{msg}</Notice>}
      {selected.length > 0 && (
        <div
          role="toolbar"
          aria-label={t("explore.selected", { count: selected.length })}
          className="sticky top-14 z-20 flex flex-wrap items-center gap-3 rounded-sm border border-accent bg-accent-wash px-3 py-2"
        >
          <span className="font-mono text-sm">{t("explore.selected", { count: selected.length })}</span>
          <Button size="sm" variant="primary" disabled={selected.length < 2} onClick={() => setMerging(true)}>
            <Merge aria-hidden />
            {t("explore.merge")}
          </Button>
          <Button size="sm" variant="ghost" onClick={() => setSelected([])}>
            <X aria-hidden />
            {t("explore.clear")}
          </Button>
        </div>
      )}

      {list.isPending ? (
        <SkeletonRows label={t("app.loading")} rows={6} />
      ) : list.isError ? (
        <Notice tone="danger">{t("app.error")}</Notice>
      ) : rows.length === 0 ? (
        <Empty>{t("explore.empty")}</Empty>
      ) : (
        <Table>
          <thead>
            <tr>
              <Th className="w-8">
                <input
                  type="checkbox"
                  aria-label={t("explore.selectAll")}
                  checked={allSelected}
                  onChange={() => setSelected(allSelected ? [] : rows.map((r) => r.key))}
                  className="size-4 accent-accent"
                />
              </Th>
              <Th>{t("explore.colName")}</Th>
              <Th>{t("explore.colType")}</Th>
              <Th className="hidden sm:table-cell">{t("explore.colDomain")}</Th>
              <Th className="hidden sm:table-cell">{t("explore.colStatus")}</Th>
              <Th className="hidden md:table-cell">{t("explore.colChanged")}</Th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => (
              <tr
                key={r.key}
                data-testid="entity-row"
                data-changed={r.changedAt ?? ""}
                aria-selected={selected.includes(r.key)}
              >
                <Td>
                  <input
                    type="checkbox"
                    aria-label={t("explore.select", { name: r.name })}
                    checked={selected.includes(r.key)}
                    onChange={() => toggle(r.key)}
                    className="size-4 accent-accent"
                  />
                </Td>
                <Td>
                  <Link
                    to="/e/$key"
                    params={{ key: r.key }}
                    className="font-medium break-words text-accent underline-offset-2 hover:underline"
                  >
                    {r.name}
                  </Link>
                  <div className="font-mono text-xs break-all text-ink-faint">{r.key}</div>
                </Td>
                <Td className="whitespace-nowrap">{typeLabel(r.type)}</Td>
                <Td className="hidden sm:table-cell">{r.domain || t("explore.noDomain")}</Td>
                <Td className="hidden sm:table-cell">
                  {r.status === "proposed" ? (
                    <Badge tone="stale">{t("explore.proposed")}</Badge>
                  ) : (
                    <Badge>{t("explore.active")}</Badge>
                  )}
                </Td>
                <Td className="hidden font-mono text-sm whitespace-nowrap text-ink-muted md:table-cell">
                  {r.changedAt ? formatAgo(r.changedAt) : ""}
                </Td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}

      {merging && (
        <MergeDialog
          title={t("explore.mergeTitle", { count: selected.length })}
          keys={selected}
          onClose={() => setMerging(false)}
          onDone={(m) => {
            setMerging(false);
            setSelected([]);
            setMsg(m);
            qc.invalidateQueries({ queryKey: ["entities"] });
          }}
        />
      )}
    </div>
  );
}
