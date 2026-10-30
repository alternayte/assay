import { useQuery } from "@tanstack/react-query";
import { ArrowUpRight } from "lucide-react";
import { useState } from "react";
import { api, type Schemas, unwrap } from "@/api/client";
import { Badge } from "@/components/ui/badge";
import { Field, Select } from "@/components/ui/field";
import { Empty } from "@/components/ui/sheet";
import { SkeletonRows } from "@/components/ui/skeleton";
import { type CopyKey, t } from "@/copy";
import { formatAgo } from "@/lib/format";
import { ClaimStatus, EntityLink } from "@/routes/entity";

type Update = Schemas["EntityUpdate"];

const predicate = (p: string) => p.replaceAll("_", " ");

function EntityChanges({ u }: { u: Update }) {
  return (
    <li className="flex flex-col gap-2 border-b border-rule py-3" data-testid="update">
      <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1">
        <h2 className="flex flex-wrap items-baseline gap-2 text-lg font-semibold">
          <EntityLink entityKey={u.key}>{u.name}</EntityLink>
          <span className="text-xs font-normal text-ink-faint">{t(`entityTypes.${u.type}` as CopyKey)}</span>
          {u.newTerm && <Badge tone="accent">{t("updates.newTerm")}</Badge>}
        </h2>
        <span className="font-mono text-xs text-ink-muted">{formatAgo(u.at)}</span>
      </div>
      {u.items.length > 0 && (
        <ul className="flex flex-col gap-1">
          {u.items.map((i) => (
            <li key={i.id} className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
              <Badge tone={i.changed ? "stale" : "supported"}>
                {i.changed ? t("updates.changed") : t("updates.new")}
              </Badge>
              <a
                href={i.url}
                target="_blank"
                rel="noopener noreferrer"
                className="inline-flex items-center gap-1 break-all text-accent underline-offset-2 hover:underline"
              >
                {i.title}
                <ArrowUpRight className="size-3.5 shrink-0" aria-hidden />
              </a>
              <span className="font-mono text-xs text-ink-faint">{i.connection}</span>
            </li>
          ))}
        </ul>
      )}
      {u.conflicts.length > 0 && (
        <ul className="flex flex-col gap-1 rounded-sm border-l-4 border-conflict bg-wash-conflict px-3 py-1.5">
          {u.conflicts.map((c) => (
            <li key={c.id} className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
              <span className="label">{t("updates.conflict")}</span>
              <span>
                {predicate(c.predicate)}: {c.objectName || c.objectText}
              </span>
              <ClaimStatus status={c.status} />
            </li>
          ))}
        </ul>
      )}
      {u.cards.length > 0 && (
        <ul className="flex flex-col gap-1">
          {u.cards.map((c) => (
            <li key={c.id} className="text-sm text-ink-muted">
              {t("updates.card", {
                kind: t(`review.kinds.${c.kind}` as CopyKey),
                status: t(`review.statuses.${c.status}` as CopyKey),
              })}
              {c.reason && ` · ${c.reason}`}
            </li>
          ))}
        </ul>
      )}
    </li>
  );
}

/** Updates: the recent visible changes, grouped per entity (REQ-REV-006). */
export function UpdatesPage() {
  const [days, setDays] = useState(7);
  const list = useQuery({
    queryKey: ["updates", days],
    queryFn: async () => unwrap(await api.GET("/api/v1/updates", { params: { query: { days } } })).entities,
  });
  return (
    <div className="flex flex-col gap-6">
      <header className="flex flex-wrap items-end justify-between gap-4 border-b border-rule-strong pb-4">
        <div className="flex flex-col gap-1">
          <h1 className="text-3xl font-semibold tracking-tight">{t("updates.title")}</h1>
          <p className="text-base text-ink-muted">{t("updates.body")}</p>
        </div>
        <Field label={t("updates.period")} className="w-48">
          {(p) => (
            <Select {...p} value={days} onChange={(e) => setDays(Number(e.target.value))}>
              <option value={7}>{t("updates.days7")}</option>
              <option value={30}>{t("updates.days30")}</option>
              <option value={90}>{t("updates.days90")}</option>
            </Select>
          )}
        </Field>
      </header>
      {list.isPending ? (
        <SkeletonRows label={t("app.loading")} rows={8} />
      ) : list.isError ? (
        <Empty>{t("app.error")}</Empty>
      ) : list.data.length === 0 ? (
        <Empty>{t("updates.empty")}</Empty>
      ) : (
        <ul className="flex flex-col">
          {list.data.map((u) => (
            <EntityChanges key={u.key} u={u} />
          ))}
        </ul>
      )}
    </div>
  );
}
