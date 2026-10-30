import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useParams } from "@tanstack/react-router";
import { ArrowUpRight, RefreshCw } from "lucide-react";
import type * as React from "react";
import { useEffect, useRef, useState } from "react";
import Markdown, { type Components } from "react-markdown";
import rehypeSanitize from "rehype-sanitize";
import { ApiError, api, type Schemas, unwrap } from "@/api/client";
import { MergeActions, Merges } from "@/components/merge";
import { type Linked, linkMarkdown, TermLink, termOf } from "@/components/terms";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Notice } from "@/components/ui/notice";
import { Empty, Sheet } from "@/components/ui/sheet";
import { Skeleton, SkeletonRows } from "@/components/ui/skeleton";
import { type CopyKey, t } from "@/copy";
import { formatAgo, formatTime } from "@/lib/format";
import { atLeast, useMe } from "@/lib/session";
import { readEvents } from "@/lib/sse";
import { claimText, Drawer, External, type Opened, sourceType } from "@/routes/ask";
import { TermFields } from "@/routes/glossary";

type Page = Schemas["Page"];
type Claim = Schemas["Claim"];
type PageLink = Schemas["PageLink"];
type Summary = Schemas["PageSummary"];

const typeLabel = (k: string) => t(`entityTypes.${k}` as CopyKey);
const predicate = (p: string) => p.replaceAll("_", " ");

const statusTone = {
  supported: "supported",
  conflicting: "conflict",
  stale: "stale",
  superseded: "neutral",
  proposed: "accent",
} as const;

/** The status of a claim for the reader, always as text. */
export function ClaimStatus({ status, noted }: { status: Claim["status"]; noted?: boolean }) {
  return (
    <Badge tone={statusTone[status]}>
      {t(`page.status.${status}` as CopyKey)}
      {noted ? ` · ${t("page.conflictNoted")}` : ""}
    </Badge>
  );
}

/** A link to the knowledge page of an entity. */
export function EntityLink({ entityKey, children }: { entityKey: string; children: React.ReactNode }) {
  return (
    <Link
      to="/e/$key"
      params={{ key: entityKey }}
      className="break-words text-accent underline-offset-2 hover:underline"
    >
      {children}
    </Link>
  );
}

/** The live state of a compile: the streamed draft until the page arrives. */
type Compile = { draft: string; running: boolean; failed?: boolean };

/** The summary Markdown. Markers such as [C1] become chips that open the claim. */
function SummaryText({ summary, onOpen }: { summary: Summary; onOpen: (o: Opened) => void }) {
  const linked = linkMarkdown(summary.markdown, summary.links.links).replace(/\[(C\d+)\]/g, "[$1](#cite-$1)");
  const terms: Linked = summary.links;
  const a: Components["a"] = ({ href, children }) => {
    const term = termOf(href, terms);
    if (term) return <TermLink terms={term}>{children}</TermLink>;
    const id = href?.startsWith("#cite-") ? href.slice(6) : undefined;
    const claim = summary.citations.find((c) => c.id === id);
    if (!claim) return <External href={href}>{children}</External>;
    return (
      <button
        type="button"
        onClick={() => onOpen({ kind: "claim", claim })}
        aria-label={t("ask.citeClaim", { id: id ?? "" })}
        title={claimText(claim)}
        className="mx-0.5 inline-flex h-5 items-center rounded-sm border border-rule-strong px-1 align-baseline font-mono text-xs text-ink-muted transition-ink hover:bg-paper-sunk hover:text-ink"
      >
        {id}
      </button>
    );
  };
  return (
    <div className="prose-register text-lg leading-7" data-testid="summary">
      <Markdown rehypePlugins={[rehypeSanitize]} components={{ a }}>
        {linked}
      </Markdown>
    </div>
  );
}

function SummarySheet({
  page,
  live,
  onOpen,
  actions,
}: {
  page: Page;
  live?: Compile;
  onOpen: (o: Opened) => void;
  actions?: React.ReactNode;
}) {
  const s = page.summary;
  let body: React.ReactNode;
  if (live?.running) {
    body = (
      <div className="flex flex-col gap-3" aria-busy="true">
        {live.draft ? (
          <p className="text-lg leading-7 text-ink-muted" data-testid="summary-draft">
            {live.draft}
          </p>
        ) : (
          <div data-testid="summary-skeleton" className="flex flex-col gap-2">
            <span className="sr-only">{t("page.compiling")}</span>
            <Skeleton className="h-5 w-full" />
            <Skeleton className="h-5 w-11/12" />
            <Skeleton className="h-5 w-3/4" />
          </div>
        )}
      </div>
    );
  } else if (live?.failed) {
    body = <Notice tone="danger">{t("page.compileError")}</Notice>;
  } else if (s.state === "fresh") {
    body = <SummaryText summary={s} onOpen={onOpen} />;
  } else if (s.state === "insufficient") {
    body = <p className="text-ink-muted">{t("page.insufficient")}</p>;
  } else if (s.state === "needs_edit") {
    body = <Notice>{t("page.needsEdit")}</Notice>;
  } else if (s.state === "off" || s.state === "failed") {
    body = <Notice>{s.notice || t("page.off")}</Notice>;
  } else {
    body = <SkeletonRows label={t("page.compiling")} rows={3} />;
  }
  return (
    <Sheet title={t("page.summary")} description={t("page.summaryBody")} actions={actions}>
      {body}
      {s.compiledAt && !live?.running && (
        <p className="mt-3 font-mono text-xs text-ink-faint" data-testid="compiled-at" data-at={s.compiledAt}>
          {t("page.compiled", { when: formatTime(s.compiledAt), model: s.model || "—" })}
        </p>
      )}
    </Sheet>
  );
}

function Evidence({ claim, onOpen }: { claim: Claim; onOpen: (o: Opened) => void }) {
  const n = new Set(claim.evidence.map((e) => e.itemId)).size;
  return (
    <button
      type="button"
      onClick={() => onOpen({ kind: "claim", claim })}
      className="font-mono text-xs whitespace-nowrap text-accent underline-offset-2 hover:underline"
    >
      {t("page.sources", { count: n })}
    </button>
  );
}

function Value({ claim }: { claim: Claim }) {
  if (claim.objectKey) return <EntityLink entityKey={claim.objectKey}>{claim.objectName}</EntityLink>;
  return <span className="break-words">{claim.objectText}</span>;
}

function Facts({ claims, onOpen }: { claims: Claim[]; onOpen: (o: Opened) => void }) {
  if (claims.length === 0) return <Empty>{t("page.noFacts")}</Empty>;
  return (
    <dl className="grid gap-x-6 gap-y-3 sm:grid-cols-[max-content_1fr]">
      {claims.map((c) => (
        <div key={c.id} className="contents" data-testid="fact">
          <dt className="label pt-0.5">{predicate(c.predicate)}</dt>
          <dd className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
            <Value claim={c} />
            <ClaimStatus status={c.status} noted={c.conflictNoted} />
            <Evidence claim={c} onOpen={onOpen} />
          </dd>
        </div>
      ))}
    </dl>
  );
}

function Conflicts({ page, onOpen }: { page: Page; onOpen: (o: Opened) => void }) {
  if (page.conflicts.length === 0) return <Empty>{t("page.noConflicts")}</Empty>;
  const all = [...page.facts, ...page.timeline];
  return (
    <ul className="flex flex-col gap-3">
      {page.conflicts.map((cf) => (
        <li
          key={cf.claims.join("-")}
          className="flex flex-col gap-2 rounded-sm border-l-4 border-conflict bg-wash-conflict px-3 py-2"
        >
          <span className="font-medium">
            {cf.subjectName} · {predicate(cf.predicate)}
          </span>
          <ul className="flex flex-wrap gap-2">
            {cf.claims.map((id) => {
              const c = all.find((x) => x.id === id);
              if (!c) return null;
              return (
                <li key={id}>
                  <button
                    type="button"
                    onClick={() => onOpen({ kind: "claim", claim: c })}
                    className="flex items-center gap-2 rounded-sm border border-conflict bg-paper-raised px-2 py-0.5 text-sm hover:bg-paper"
                  >
                    {c.objectName || c.objectText}
                    <ClaimStatus status={c.status} noted={c.conflictNoted} />
                  </button>
                </li>
              );
            })}
          </ul>
        </li>
      ))}
    </ul>
  );
}

function Links({ links, empty }: { links: PageLink[]; empty: string }) {
  if (links.length === 0) return <Empty>{empty}</Empty>;
  return (
    <ul className="flex flex-col divide-y divide-rule">
      {links.map((l) => (
        <li
          key={`${l.claimId}-${l.direction}`}
          className="flex flex-wrap items-baseline gap-x-3 gap-y-1 py-1.5"
        >
          <span className="label w-40 shrink-0">
            {l.direction === "in"
              ? t("page.incoming", { predicate: predicate(l.predicate) })
              : predicate(l.predicate)}
          </span>
          <EntityLink entityKey={l.key}>{l.name}</EntityLink>
          <span className="text-xs text-ink-faint">{typeLabel(l.type)}</span>
          {l.status !== "supported" && <ClaimStatus status={l.status} />}
        </li>
      ))}
    </ul>
  );
}

function Timeline({ claims, onOpen }: { claims: Claim[]; onOpen: (o: Opened) => void }) {
  if (claims.length === 0) return <Empty>{t("page.noTimeline")}</Empty>;
  return (
    <ol className="flex flex-col">
      {claims.map((c) => (
        <li
          key={c.id}
          className="grid grid-cols-[6.5rem_1fr] gap-3 border-l-2 border-rule py-1.5 pl-3 data-[current=true]:border-accent"
          data-current={c.current}
        >
          <span className="font-mono text-sm text-ink-muted">
            {c.validFrom ? formatTime(c.validFrom).split(",")[0] : "—"}
          </span>
          <span className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
            <span className="label">{predicate(c.predicate)}</span>
            <Value claim={c} />
            {c.current ? (
              <Badge tone="accent">{t("page.current")}</Badge>
            ) : (
              <Badge>{t("page.earlier")}</Badge>
            )}
            <Evidence claim={c} onOpen={onOpen} />
          </span>
        </li>
      ))}
    </ol>
  );
}

function Sources({ page }: { page: Page }) {
  if (page.sources.length === 0) return <Empty>{t("page.noSources")}</Empty>;
  return (
    <ul className="flex flex-col divide-y divide-rule">
      {page.sources.map((s) => (
        <li key={s.itemId} className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1 py-1.5">
          <a
            href={s.url}
            target="_blank"
            rel="noopener noreferrer"
            className="inline-flex items-center gap-1 break-all text-accent underline-offset-2 hover:underline"
          >
            {s.title}
            <ArrowUpRight className="size-3.5 shrink-0" aria-hidden />
          </a>
          <span className="font-mono text-xs text-ink-muted">
            {[sourceType(s.sourceType), s.connectionName, s.updatedAt ? formatAgo(s.updatedAt) : ""]
              .filter(Boolean)
              .join(" · ")}
          </span>
        </li>
      ))}
    </ul>
  );
}

function Meta({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex flex-col gap-0.5">
      <dt className="label">{label}</dt>
      <dd>{children}</dd>
    </div>
  );
}

/** The knowledge page of an entity: sections from claims first, then the summary. */
export function EntityPage() {
  const { key } = useParams({ from: "/authed/e/$key" });
  const me = useMe();
  const qc = useQueryClient();
  const queryKey = ["page", key];
  const page = useQuery({
    queryKey,
    queryFn: async () => unwrap(await api.GET("/api/v1/entities/{key}/page", { params: { path: { key } } })),
    retry: (n, e) => !(e instanceof ApiError && e.status === 404) && n < 2,
  });
  const [live, setLive] = useState<Compile>();
  const [opened, setOpened] = useState<Opened>();
  const [regenerating, setRegenerating] = useState(false);
  const detail = useQuery({
    queryKey: ["entity", key],
    queryFn: async () => unwrap(await api.GET("/api/v1/entities/{key}", { params: { path: { key } } })),
    retry: false,
  });
  const refresh = () => {
    qc.invalidateQueries({ queryKey: ["entity", key] });
    qc.invalidateQueries({ queryKey: ["page", key] });
  };
  const started = useRef<string>(undefined);

  const state = page.data?.summary.state;
  useEffect(() => {
    if ((state !== "missing" && state !== "stale") || started.current === key) return;
    started.current = key;
    const ctl = new AbortController();
    let draft = "";
    setLive({ draft, running: true });
    (async () => {
      try {
        const r = await api.POST("/api/v1/entities/{key}/page/compile", {
          params: { path: { key } },
          parseAs: "stream",
          signal: ctl.signal,
        });
        if (!r.response.ok || !r.data) throw new Error(String(r.response.status));
        let done = false;
        await readEvents(r.data as ReadableStream<Uint8Array>, (ev) => {
          const data = JSON.parse(ev.data);
          if (ev.event === "delta") {
            draft += data.text;
            setLive({ draft, running: true });
          }
          if (ev.event === "retry") {
            draft = "";
            setLive({ draft, running: true });
          }
          if (ev.event === "page") {
            done = true;
            qc.setQueryData(["page", key], data);
            setLive(undefined);
          }
        });
        if (!done) setLive({ draft: "", running: false, failed: true });
      } catch {
        if (!ctl.signal.aborted) setLive({ draft: "", running: false, failed: true });
      }
    })();
    return () => {
      ctl.abort();
      started.current = undefined;
    };
  }, [key, state, qc]);

  const regenerate = async () => {
    setRegenerating(true);
    try {
      const p = unwrap(
        await api.POST("/api/v1/entities/{key}/page/regenerate", { params: { path: { key } } }),
      );
      qc.setQueryData(queryKey, p);
    } catch {
      setLive({ draft: "", running: false, failed: true });
    } finally {
      setRegenerating(false);
    }
  };

  if (page.isPending) return <SkeletonRows label={t("app.loading")} rows={10} />;
  if (page.isError) {
    return (
      <Empty
        action={
          <Button asChild variant="primary">
            <Link to="/explore">{t("page.toExplore")}</Link>
          </Button>
        }
      >
        {page.error instanceof ApiError && page.error.status === 404 ? t("page.notFound") : t("app.error")}
      </Empty>
    );
  }
  const p = page.data;
  const reviewer = atLeast(me, "reviewer");
  return (
    <div className="flex flex-col gap-6">
      <header className="flex flex-col gap-3 border-b border-rule-strong pb-4">
        <Link to="/explore" className="text-sm text-accent underline-offset-2 hover:underline">
          {t("page.back")}
        </Link>
        <div className="flex flex-wrap items-baseline gap-3">
          <h1 className="text-3xl font-semibold tracking-tight break-words">{p.name}</h1>
          <Badge>{typeLabel(p.type)}</Badge>
        </div>
        <p className="font-mono text-xs break-all text-ink-faint">{p.key}</p>
        {p.aliases.length > 0 && (
          <p className="text-sm text-ink-muted">{t("page.aliases", { aliases: p.aliases.join(", ") })}</p>
        )}
        <dl className="grid grid-cols-2 gap-x-6 gap-y-3 sm:grid-cols-4">
          <Meta label={t("page.owner")}>
            {p.owners.length === 0
              ? "—"
              : p.owners.map((o, i) => (
                  <span key={o.claimId}>
                    {i > 0 && ", "}
                    <EntityLink entityKey={o.key}>{o.name}</EntityLink>
                  </span>
                ))}
          </Meta>
          <Meta label={t("page.domain")}>{p.domain || t("page.noDomain")}</Meta>
          <Meta label={t("page.newest")}>
            <span className="font-mono text-sm">{p.newest ? formatAgo(p.newest) : "—"}</span>
          </Meta>
          <Meta label={t("page.permissions")}>
            <span className="font-mono text-sm" data-testid="permissions-checked">
              {p.permissionsCheckedAt ? formatAgo(p.permissionsCheckedAt) : t("page.notChecked")}
            </span>
          </Meta>
        </dl>
        <MergeActions entity={{ key: p.key, name: p.name }} onChange={refresh} />
        {detail.data && <Merges merges={detail.data.merges} onSplit={refresh} />}
        {p.type === "term" && (
          <Link
            to="/glossary/$key"
            params={{ key: p.key }}
            className="text-sm text-accent underline-offset-2 hover:underline"
          >
            {t("page.openTerm")}
          </Link>
        )}
      </header>

      <SummarySheet
        page={p}
        live={live}
        onOpen={setOpened}
        actions={
          reviewer && (
            <Button onClick={regenerate} disabled={regenerating || live?.running}>
              <RefreshCw className="size-4" aria-hidden />
              {regenerating ? t("page.regenerating") : t("page.regenerate")}
            </Button>
          )
        }
      />
      {p.type === "term" && <TermFields termKey={p.key} />}
      <Sheet title={t("page.facts")} description={t("page.factsBody")}>
        <Facts claims={p.facts} onOpen={setOpened} />
      </Sheet>
      <Sheet title={t("page.conflicts")} description={t("page.conflictsBody")}>
        <Conflicts page={p} onOpen={setOpened} />
      </Sheet>
      <div className="grid gap-6 lg:grid-cols-2">
        <Sheet title={t("page.decisions")}>
          <Links links={p.decisions} empty={t("page.noDecisions")} />
        </Sheet>
        <Sheet title={t("page.timeline")} description={t("page.timelineBody")}>
          <Timeline claims={p.timeline} onOpen={setOpened} />
        </Sheet>
      </div>
      <Sheet title={t("page.related")}>
        <Links links={p.related} empty={t("page.noRelated")} />
      </Sheet>
      <Sheet title={t("page.sourcesTitle")}>
        <Sources page={p} />
      </Sheet>
      <Drawer opened={opened} onClose={() => setOpened(undefined)} />
    </div>
  );
}
