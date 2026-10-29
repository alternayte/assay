import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import { ArrowUpRight, CornerDownLeft, X } from "lucide-react";
import { Dialog as D } from "radix-ui";
import type * as React from "react";
import { type FormEvent, useEffect, useRef, useState } from "react";
import Markdown, { type Components } from "react-markdown";
import rehypeSanitize from "rehype-sanitize";
import { api, type Schemas, unwrap } from "@/api/client";
import { type Linked, linkMarkdown, splitLinks, TermLink, termOf } from "@/components/terms";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Field } from "@/components/ui/field";
import { Notice } from "@/components/ui/notice";
import { Empty, Sheet } from "@/components/ui/sheet";
import { SkeletonRows } from "@/components/ui/skeleton";
import { type CopyKey, t } from "@/copy";
import { cn } from "@/lib/cn";
import { formatAgo, formatTime } from "@/lib/format";
import { readEvents } from "@/lib/sse";

type Answer = Schemas["Answer"];
type Result = Schemas["Result"];
type Entity = Schemas["Entity"];
type Source = Schemas["Source"];
type Claim = Schemas["ContextClaim"];
type Segment = Schemas["Segment"];

export const sourceType = (k: string) => t(`ask.sourceTypes.${k}` as CopyKey);

/** A claim as the drawer shows it: an answer claim or a page claim. */
export type ClaimLike = Pick<Claim, "subjectName" | "predicate" | "objectName" | "objectText" | "evidence">;

/** The open drawer: a source or a claim. */
export type Opened = { kind: "source"; source: Source } | { kind: "claim"; claim: ClaimLike };

/** Links outside Assay open in a new tab without the opener or referrer. */
export function External({ href, children }: { href?: string; children?: React.ReactNode }) {
  return (
    <a
      href={href}
      target="_blank"
      rel="noopener noreferrer"
      className="text-accent underline underline-offset-2"
    >
      {children}
    </a>
  );
}
const external: Components["a"] = ({ href, children }) => <External href={href}>{children}</External>;

/** A link renderer that shows term links as popovers and others as external links. */
function linkWith(linked?: Linked): Components["a"] {
  return ({ href, children }) => {
    const terms = termOf(href, linked);
    if (terms) return <TermLink terms={terms}>{children}</TermLink>;
    return <External href={href}>{children}</External>;
  };
}

/** Source text from a connection: sanitized Markdown, no raw HTML. Approved terms link. */
function SourceText({ text, linked }: { text: string; linked?: Linked }) {
  return (
    <div className="prose-register">
      <Markdown rehypePlugins={[rehypeSanitize]} components={{ a: linked ? linkWith(linked) : external }}>
        {linked ? linkMarkdown(text, linked.links) : text}
      </Markdown>
    </div>
  );
}

function Snippet({ segments }: { segments: Segment[] }) {
  return (
    <p className="text-sm text-ink-muted">
      {segments.map((s, i) =>
        s.hit ? (
          // biome-ignore lint/suspicious/noArrayIndexKey: segments have no other identity.
          <mark key={i} className="rounded-sm bg-accent-wash px-0.5 text-ink">
            {s.text}
          </mark>
        ) : (
          // biome-ignore lint/suspicious/noArrayIndexKey: segments have no other identity.
          <span key={i}>{s.text}</span>
        ),
      )}
    </p>
  );
}

/** The answer Markdown. Markers such as [S1] become citation chips. */
function AnswerText({
  markdown,
  sources,
  claims,
  terms,
  onOpen,
}: {
  markdown: string;
  sources: Source[];
  claims: Claim[];
  terms?: Linked;
  onOpen: (o: Opened) => void;
}) {
  const linked = linkMarkdown(markdown, terms?.links ?? []).replace(/\[([SC]\d+)\]/g, "[$1](#cite-$1)");
  const chip: Components["a"] = ({ href, children }) => {
    const term = termOf(href, terms);
    if (term) return <TermLink terms={term}>{children}</TermLink>;
    const id = href?.startsWith("#cite-") ? href.slice(6) : undefined;
    const source = id ? sources.find((s) => s.id === id) : undefined;
    const claim = id ? claims.find((c) => c.id === id) : undefined;
    if (!source && !claim) return <External href={href}>{children}</External>;
    return (
      <button
        type="button"
        onClick={() => onOpen(source ? { kind: "source", source } : { kind: "claim", claim: claim as Claim })}
        aria-label={source ? t("ask.cite", { id: id ?? "" }) : t("ask.citeClaim", { id: id ?? "" })}
        title={source?.title ?? claimText(claim as Claim)}
        className={cn(
          "mx-0.5 inline-flex h-5 items-center rounded-sm border px-1 align-baseline font-mono text-xs transition-ink",
          source
            ? "border-accent text-accent hover:bg-accent-wash"
            : "border-rule-strong text-ink-muted hover:bg-paper-sunk hover:text-ink",
        )}
      >
        {id}
      </button>
    );
  };
  return (
    <div className="prose-register text-lg leading-7">
      <Markdown rehypePlugins={[rehypeSanitize]} components={{ a: chip }}>
        {linked}
      </Markdown>
    </div>
  );
}

export function claimText(c: ClaimLike) {
  return t("ask.claimText", {
    subject: c.subjectName,
    predicate: c.predicate.replaceAll("_", " "),
    object: c.objectName || c.objectText || "",
  });
}

function ResultList({
  entities,
  results,
  degraded,
}: {
  entities: Entity[];
  results: Result[];
  degraded: boolean;
}) {
  return (
    <div className="flex flex-col gap-4">
      {degraded && <p className="text-sm text-ink-muted">{t("ask.degraded")}</p>}
      {entities.length > 0 && (
        <div>
          <h3 className="label mb-2">{t("ask.entities")}</h3>
          <ul className="flex flex-wrap gap-2">
            {entities.map((e) => (
              <li
                key={e.key}
                className="flex items-baseline gap-2 rounded-sm border border-rule bg-paper px-2 py-1"
              >
                <span className="font-medium">{e.name}</span>
                <span className="font-mono text-xs text-ink-muted">{e.type}</span>
              </li>
            ))}
          </ul>
        </div>
      )}
      {results.length === 0 ? (
        <Empty>{t("ask.resultsEmpty")}</Empty>
      ) : (
        <ol className="flex flex-col divide-y divide-rule">
          {results.map((r) => (
            <li key={r.chunkId} className="flex flex-col gap-1 py-3 first:pt-0">
              <div className="flex flex-wrap items-baseline justify-between gap-x-3">
                <a
                  href={r.url}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="min-w-0 font-medium break-words text-ink hover:text-accent hover:underline"
                >
                  {r.title}
                </a>
                <span className="font-mono text-xs text-ink-muted">
                  {sourceType(r.sourceType)} · {r.connectionName}
                </span>
              </div>
              {r.headingPath.length > 0 && (
                <p className="font-mono text-xs text-ink-faint">{r.headingPath.join(" › ")}</p>
              )}
              <Snippet segments={r.snippet} />
            </li>
          ))}
        </ol>
      )}
    </div>
  );
}

function Blocks({ answer, onOpen }: { answer: Answer; onOpen: (o: Opened) => void }) {
  const byId = (id: string) => answer.claims.find((c) => c.id === id);
  return (
    <>
      {answer.conflicts.length > 0 && (
        <section
          aria-labelledby="ask-conflicts"
          className="rounded-sm border-l-4 border-conflict bg-wash-conflict px-3 py-2"
        >
          <h3 id="ask-conflicts" className="flex items-center gap-2 font-semibold">
            <Badge tone="conflict">{t("ask.conflicts")}</Badge>
            <span className="text-base font-normal text-ink-muted">{t("ask.conflictsBody")}</span>
          </h3>
          <ul className="mt-2 flex flex-col gap-1">
            {answer.conflicts.map((c) => (
              <li key={`${c.subjectName}-${c.predicate}`} className="flex flex-wrap items-baseline gap-2">
                <span className="font-medium">
                  {c.subjectName} · {c.predicate.replaceAll("_", " ")}
                </span>
                {c.claims.map((id) => {
                  const cl = byId(id);
                  return cl ? (
                    <button
                      key={id}
                      type="button"
                      onClick={() => onOpen({ kind: "claim", claim: cl })}
                      className="rounded-sm border border-conflict px-1.5 text-sm text-conflict hover:bg-paper-raised"
                    >
                      {cl.objectName || cl.objectText}
                    </button>
                  ) : null;
                })}
              </li>
            ))}
          </ul>
        </section>
      )}
      {answer.unknowns.length > 0 && (
        <section
          aria-labelledby="ask-unknowns"
          className="rounded-sm border-l-4 border-stale bg-wash-stale px-3 py-2"
        >
          <h3 id="ask-unknowns" className="flex items-center gap-2 font-semibold">
            <Badge tone="stale">{t("ask.unknowns")}</Badge>
            <span className="text-base font-normal text-ink-muted">{t("ask.unknownsBody")}</span>
          </h3>
          <ul className="mt-2 list-disc pl-5">
            {answer.unknowns.map((u) => (
              <li key={u}>{u}</li>
            ))}
          </ul>
        </section>
      )}
    </>
  );
}

function HowWeKnow({ answer, onOpen }: { answer: Answer; onOpen: (o: Opened) => void }) {
  const claims = answer.claims.filter((c) => answer.cited.includes(c.id));
  const sources = answer.sources.filter((s) => answer.cited.includes(s.id));
  return (
    <details className="group rounded-sm border border-rule bg-paper">
      <summary className="cursor-pointer list-none px-3 py-2 font-medium text-accent marker:hidden hover:bg-paper-sunk">
        {t("ask.howTitle")}
      </summary>
      <div className="flex flex-col gap-3 border-t border-rule px-3 py-3">
        <p className="text-sm text-ink-muted">{t("ask.howBody")}</p>
        {claims.length === 0 && sources.length === 0 && <p>{t("ask.howEmpty")}</p>}
        {claims.length > 0 && (
          <div>
            <h4 className="label mb-1">{t("ask.howClaims")}</h4>
            <ul className="flex flex-col gap-1">
              {claims.map((c) => (
                <li key={c.id} className="flex items-baseline gap-2">
                  <button
                    type="button"
                    onClick={() => onOpen({ kind: "claim", claim: c })}
                    className="font-mono text-xs text-accent underline"
                  >
                    {c.id}
                  </button>
                  <span>{claimText(c)}</span>
                  <span className="font-mono text-xs text-ink-muted">
                    {[...new Set(c.evidence.map((e) => e.connectionName))].join(", ")}
                  </span>
                </li>
              ))}
            </ul>
          </div>
        )}
        {sources.length > 0 && (
          <div>
            <h4 className="label mb-1">{t("ask.howSources")}</h4>
            <ul className="flex flex-col gap-1">
              {sources.map((s) => (
                <li key={s.id} className="flex flex-wrap items-baseline gap-2">
                  <button
                    type="button"
                    onClick={() => onOpen({ kind: "source", source: s })}
                    className="font-mono text-xs text-accent underline"
                  >
                    {s.id}
                  </button>
                  <span className="break-words">{s.title}</span>
                  <span className="font-mono text-xs text-ink-muted">
                    {sourceType(s.sourceType)} · {s.connectionName}
                  </span>
                </li>
              ))}
            </ul>
          </div>
        )}
      </div>
    </details>
  );
}

/** The evidence drawer: the item with the cited passages marked. */
export function Drawer({ opened, onClose }: { opened?: Opened; onClose: () => void }) {
  const itemId = opened?.kind === "source" ? opened.source.itemId : undefined;
  const item = useQuery({
    queryKey: ["item", itemId],
    enabled: itemId !== undefined,
    queryFn: async () =>
      unwrap(await api.GET("/api/v1/items/{id}", { params: { path: { id: itemId as number } } })),
    retry: false,
  });
  const passages = item.data?.passages ?? [];
  const sep = "\n\n";
  const links = useQuery({
    queryKey: ["item-links", itemId, passages.length],
    enabled: passages.length > 0,
    queryFn: async () => {
      const texts = passages.map((p) => p.text);
      const linked = unwrap(await api.POST("/api/v1/terms/links", { body: { text: texts.join(sep) } }));
      return splitLinks(texts, linked, sep);
    },
  });
  const marked = useRef<HTMLLIElement>(null);
  useEffect(() => {
    if (item.data) marked.current?.scrollIntoView({ block: "center" });
  }, [item.data]);
  const title = opened?.kind === "source" ? opened.source.title : opened ? claimText(opened.claim) : "";
  return (
    <D.Root open={opened !== undefined} onOpenChange={(o) => !o && onClose()}>
      <D.Portal>
        <D.Overlay className="fixed inset-0 z-40 bg-ink/30 dark:bg-paper-sunk/70" />
        <D.Content className="fixed inset-y-0 right-0 z-50 flex w-full max-w-xl flex-col border-l border-rule-strong bg-paper-raised shadow-sheet">
          <header className="flex items-start justify-between gap-3 border-b border-rule px-4 py-3">
            <div className="min-w-0">
              <D.Title className="text-lg font-semibold break-words">{title}</D.Title>
              <D.Description className="font-mono text-xs text-ink-muted">
                {opened?.kind === "source"
                  ? [
                      sourceType(opened.source.sourceType),
                      opened.source.connectionName,
                      opened.source.updatedAt
                        ? t("ask.updated", { when: formatTime(opened.source.updatedAt) })
                        : "",
                    ]
                      .filter(Boolean)
                      .join(" · ")
                  : t("ask.evidence")}
              </D.Description>
              {opened?.kind === "source" && (
                <a
                  href={opened.source.url}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="mt-1 inline-flex items-center gap-1 text-accent underline underline-offset-2"
                >
                  {t("ask.openSource")}
                  <ArrowUpRight className="size-4" aria-hidden />
                </a>
              )}
            </div>
            <D.Close
              className="rounded-sm p-1 text-ink-muted hover:bg-paper-sunk hover:text-ink"
              aria-label={t("ask.drawerClose")}
            >
              <X className="size-4" aria-hidden />
            </D.Close>
          </header>
          <div className="flex-1 overflow-y-auto px-4 py-3">
            {opened?.kind === "source" && (
              <div className="flex flex-col gap-3">
                {item.isPending ? (
                  <SkeletonRows label={t("ask.passageLoading")} rows={4} />
                ) : item.isError ? (
                  <Notice tone="danger">{t("ask.passageHidden")}</Notice>
                ) : (
                  <ol className="flex flex-col gap-2">
                    {item.data.passages.map((p, n) => {
                      const hit = opened.source.passageHashes.includes(p.hash);
                      return (
                        <li
                          key={`${p.ordinal}-${p.hash}`}
                          ref={hit && !marked.current ? marked : undefined}
                          data-cited={hit || undefined}
                          className={cn(
                            "rounded-sm px-3 py-1",
                            hit ? "border-l-4 border-accent bg-accent-wash" : "text-ink-muted",
                          )}
                        >
                          <SourceText text={p.text} linked={links.data?.[n]} />
                        </li>
                      );
                    })}
                  </ol>
                )}
              </div>
            )}
            {opened?.kind === "claim" && (
              <ul className="flex flex-col gap-3">
                {opened.claim.evidence.map((e) => (
                  <li
                    key={`${e.itemId}-${e.passageHash}-${e.stance}`}
                    className="flex flex-col gap-1 border-b border-rule pb-3"
                  >
                    <div className="flex flex-wrap items-baseline justify-between gap-2">
                      <a
                        href={e.url}
                        target="_blank"
                        rel="noopener noreferrer"
                        className="font-medium break-words text-accent underline underline-offset-2"
                      >
                        {e.title}
                      </a>
                      <span className="flex gap-1">
                        {e.stance === "disputes" && <Badge tone="conflict">{t("ask.disputes")}</Badge>}
                        <Badge tone={e.state === "current" ? "supported" : "stale"}>
                          {e.state === "current" ? t("ask.current") : t("ask.stale")}
                        </Badge>
                      </span>
                    </div>
                    <p className="font-mono text-xs text-ink-muted">
                      {sourceType(e.sourceType)} · {e.connectionName}
                    </p>
                    <blockquote className="border-l-4 border-accent bg-accent-wash px-3 py-1">
                      <mark className="bg-transparent text-ink">{e.quote}</mark>
                    </blockquote>
                  </li>
                ))}
              </ul>
            )}
          </div>
        </D.Content>
      </D.Portal>
    </D.Root>
  );
}

function History({ current }: { current?: number }) {
  const list = useQuery({
    queryKey: ["questions"],
    queryFn: async () => unwrap(await api.GET("/api/v1/questions")).questions,
  });
  return (
    <Sheet title={t("ask.history")} description={t("ask.historyBody")}>
      {list.isPending ? (
        <SkeletonRows label={t("app.loading")} rows={3} />
      ) : list.isError ? (
        <Notice tone="danger">{t("app.error")}</Notice>
      ) : list.data.length === 0 ? (
        <Empty>{t("ask.historyEmpty")}</Empty>
      ) : (
        <ol className="flex flex-col divide-y divide-rule">
          {list.data.map((q) => (
            <li key={q.id} className="py-2">
              <Link
                to="/questions/$id"
                params={{ id: String(q.id) }}
                aria-current={q.id === current ? "page" : undefined}
                className="block break-words hover:text-accent aria-[current=page]:font-medium aria-[current=page]:text-accent"
              >
                {q.text}
              </Link>
              <span className="font-mono text-xs text-ink-muted">
                {t("ask.asked", { when: formatAgo(q.createdAt) })}
              </span>
            </li>
          ))}
        </ol>
      )}
    </Sheet>
  );
}

type Live = {
  results?: Schemas["ResultsEvent"];
  draft: string;
  answer?: Answer;
  error?: boolean;
  pending: boolean;
};

function AnswerSheet({ live, onOpen }: { live: Live; onOpen: (o: Opened) => void }) {
  const a = live.answer;
  return (
    <Sheet title={t("ask.answer")} description={a?.question}>
      <div className="flex flex-col gap-4" aria-live="polite" aria-busy={live.pending}>
        {live.error && <Notice tone="danger">{t("ask.error")}</Notice>}
        {a?.notice && <Notice>{a.notice.message}</Notice>}
        {!a && live.draft === "" && live.pending && <SkeletonRows label={t("ask.asking")} rows={3} />}
        {!a && live.draft !== "" && (
          <p className="text-lg leading-7 whitespace-pre-wrap text-ink-muted">{live.draft}</p>
        )}
        {a && a.markdown !== "" && (
          <AnswerText
            markdown={a.markdown}
            sources={a.sources}
            claims={a.claims}
            terms={a.links}
            onOpen={onOpen}
          />
        )}
        {a && a.uncited.length > 0 && !a.noEvidence && (
          <p className="text-sm text-ink-muted">{t("ask.uncited", { count: a.uncited.length })}</p>
        )}
        {a && <Blocks answer={a} onOpen={onOpen} />}
        {a && a.markdown !== "" && <HowWeKnow answer={a} onOpen={onOpen} />}
      </div>
    </Sheet>
  );
}

/** Ask: the home page. A question streams its answer; an ID opens a stored one. */
export function AskPage() {
  const { id } = useParams({ strict: false }) as { id?: string };
  const navigate = useNavigate();
  const qc = useQueryClient();
  const [question, setQuestion] = useState("");
  const [live, setLive] = useState<Live>();
  const [opened, setOpened] = useState<Opened>();
  const abort = useRef<AbortController>(null);

  const stored = useQuery({
    queryKey: ["question", id],
    enabled: id !== undefined && live === undefined,
    queryFn: async () =>
      unwrap(await api.GET("/api/v1/questions/{id}", { params: { path: { id: Number(id) } } })),
  });

  useEffect(() => () => abort.current?.abort(), []);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const q = question.trim();
    if (!q) return;
    abort.current?.abort();
    const ctl = new AbortController();
    abort.current = ctl;
    let state: Live = { draft: "", pending: true };
    const update = (next: Partial<Live>) => {
      state = { ...state, ...next };
      setLive(state);
    };
    update({});
    try {
      const r = await api.POST("/api/v1/ask", {
        body: { question: q },
        parseAs: "stream",
        signal: ctl.signal,
      });
      if (!r.response.ok || !r.data) throw new Error(String(r.response.status));
      await readEvents(r.data as ReadableStream<Uint8Array>, (ev) => {
        const data = JSON.parse(ev.data);
        if (ev.event === "results") update({ results: data });
        if (ev.event === "delta") update({ draft: state.draft + data.text });
        if (ev.event === "answer") update({ answer: data, pending: false });
        if (ev.event === "error") update({ error: true, pending: false });
      });
      update({ pending: false });
      if (state.answer) {
        qc.invalidateQueries({ queryKey: ["questions"] });
        qc.setQueryData(["question", String(state.answer.questionId)], state.answer);
        navigate({ to: "/questions/$id", params: { id: String(state.answer.questionId) } });
      }
    } catch {
      if (!ctl.signal.aborted) update({ error: true, pending: false });
    }
  };

  // A stored question shows as a finished live answer.
  const shown: Live | undefined =
    live ??
    (stored.data
      ? {
          draft: "",
          pending: false,
          answer: stored.data,
          results: { entities: [], results: stored.data.results, degraded: false },
        }
      : undefined);
  useEffect(() => {
    if (id === undefined) setLive(undefined);
  }, [id]);
  useEffect(() => {
    if (live?.answer && id === String(live.answer.questionId)) setLive(undefined);
  }, [id, live]);

  return (
    <div className="flex flex-col gap-6">
      <header className="flex flex-col gap-1">
        <h1 className="text-3xl font-semibold tracking-tight">{t("ask.title")}</h1>
        <p className="text-base text-ink-muted">{t("ask.body")}</p>
      </header>
      <form onSubmit={submit} className="flex flex-col gap-2 sm:flex-row sm:items-end">
        <Field label={t("ask.label")} className="flex-1">
          {(p) => (
            <input
              {...p}
              type="search"
              required
              maxLength={1000}
              autoComplete="off"
              placeholder={t("ask.placeholder")}
              value={question}
              onChange={(e) => setQuestion(e.target.value)}
              className="h-11 w-full rounded-sm border border-rule-strong bg-paper-raised px-3 text-lg text-ink placeholder:text-ink-faint transition-ink focus-visible:border-accent"
            />
          )}
        </Field>
        <Button type="submit" variant="primary" disabled={shown?.pending} className="h-11">
          <CornerDownLeft aria-hidden />
          {shown?.pending ? t("ask.asking") : t("ask.submit")}
        </Button>
      </form>
      <div className="grid items-start gap-6 lg:grid-cols-[minmax(0,3fr)_minmax(0,2fr)]">
        <div className="flex min-w-0 flex-col gap-6">
          {id !== undefined && !shown && stored.isPending && (
            <SkeletonRows label={t("app.loading")} rows={4} />
          )}
          {id !== undefined && stored.isError && !live && (
            <Notice tone="danger">{t("app.notFoundBody")}</Notice>
          )}
          {shown && <AnswerSheet live={shown} onOpen={setOpened} />}
          {shown?.results && (
            <Sheet title={t("ask.results")}>
              <ResultList
                entities={shown.results.entities}
                results={shown.results.results}
                degraded={shown.results.degraded}
              />
            </Sheet>
          )}
        </div>
        <History current={id ? Number(id) : undefined} />
      </div>
      <Drawer opened={opened} onClose={() => setOpened(undefined)} />
    </div>
  );
}
