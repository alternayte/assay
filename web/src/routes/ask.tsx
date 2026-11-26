import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import { ArrowUpRight, CornerDownLeft, Plus, ThumbsDown, ThumbsUp, X } from "lucide-react";
import { Dialog as D } from "radix-ui";
import type * as React from "react";
import { type FormEvent, useEffect, useId, useRef, useState } from "react";
import Markdown, { type Components } from "react-markdown";
import rehypeSanitize from "rehype-sanitize";
import { ApiError, api, type Schemas, unwrap } from "@/api/client";
import { ProposeFinding } from "@/components/finding";
import { ReportProblem } from "@/components/report";
import { type Linked, linkMarkdown, splitLinks, TermLink, termOf } from "@/components/terms";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Field, Textarea } from "@/components/ui/field";
import { Notice } from "@/components/ui/notice";
import { Empty, Sheet } from "@/components/ui/sheet";
import { SkeletonRows } from "@/components/ui/skeleton";
import { type CopyKey, t } from "@/copy";
import { cn } from "@/lib/cn";
import { formatAgo, formatTime } from "@/lib/format";
import { readEvents } from "@/lib/sse";

type Answer = Schemas["Answer"];
type Thread = Schemas["Thread"];
type Feedback = Pick<Schemas["Feedback"], "rating" | "reason" | "wrongCitation">;
type Result = Schemas["Result"];
type Entity = Schemas["Entity"];
type Source = Schemas["Source"];
type Claim = Schemas["ContextClaim"];
type Segment = Schemas["Segment"];

export const sourceType = (k: string) => t(`ask.sourceTypes.${k}` as CopyKey);

/** A claim as the drawer shows it: an answer claim or a page claim. */
export type ClaimLike = Pick<
  Claim,
  "subjectName" | "predicate" | "objectName" | "objectText" | "evidence"
> & {
  id?: number | string;
  claimId?: number;
  objectKey?: string;
};

/** The numeric ID of a claim: a page claim carries it as id, a cited claim as claimId. */
const claimIdOf = (c: ClaimLike) => (typeof c.id === "number" ? c.id : c.claimId || undefined);

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

function HowWeKnow({ answer, onOpen, down }: { answer: Answer; onOpen: (o: Opened) => void; down: boolean }) {
  const claims = answer.claims.filter((c) => answer.cited.includes(c.id));
  const sources = answer.sources.filter((s) => answer.cited.includes(s.id));
  return (
    <details key={down ? "down" : "up"} open={down} className="group rounded-sm border border-rule bg-paper">
      <summary className="cursor-pointer list-none px-3 py-2 font-medium text-accent marker:hidden hover:bg-paper-sunk">
        {t("ask.howTitle")}
      </summary>
      <div className="flex flex-col gap-3 border-t border-rule px-3 py-3">
        <p className="text-sm text-ink-muted">{down ? t("feedback.fixHint") : t("ask.howBody")}</p>
        {claims.length === 0 && sources.length === 0 && <p>{t("ask.howEmpty")}</p>}
        {claims.length > 0 && (
          <div>
            <h4 className="label mb-1">{t("ask.howClaims")}</h4>
            <ul className="flex flex-col gap-1">
              {claims.map((c) => (
                <li key={c.id} className="flex flex-wrap items-baseline gap-2">
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
                  {down && (
                    <ReportProblem
                      claim={{ id: c.claimId, title: claimText(c), entity: Boolean(c.objectKey) }}
                    />
                  )}
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
                  {down && <ProposeFinding source={{ itemId: s.itemId, title: s.title }} />}
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
            {opened?.kind === "claim" && claimIdOf(opened.claim) !== undefined && (
              <div className="mt-3">
                <ReportProblem
                  key={claimIdOf(opened.claim)}
                  claim={{
                    id: claimIdOf(opened.claim) as number,
                    title: claimText(opened.claim),
                    entity: Boolean(opened.claim.objectKey),
                  }}
                />
              </div>
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
                {q.turns > 1
                  ? `${t("ask.historyTurns", { count: q.turns })} · ${t("ask.lastAsked", { when: formatAgo(q.lastAt) })}`
                  : t("ask.asked", { when: formatAgo(q.lastAt) })}
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
  question: string;
};

/** The marks of the asker on one answer. A down mark takes a reason and the wrong-citation flag. */
function Marks({
  answer,
  privacy,
  onSaved,
}: {
  answer: Answer;
  privacy: boolean;
  onSaved: (f: Feedback) => void;
}) {
  const mark = answer.feedback;
  const [reason, setReason] = useState(mark?.reason ?? "");
  const [wrong, setWrong] = useState(mark?.wrongCitation ?? false);
  const [saved, setSaved] = useState(false);
  const save = useMutation({
    mutationFn: async (f: Feedback) =>
      unwrap(
        await api.PUT("/api/v1/questions/{id}/feedback", {
          params: { path: { id: answer.questionId } },
          body: f,
        }),
      ),
    onSuccess: (f) => {
      setSaved(true);
      onSaved(f);
    },
  });
  const rate = (rating: "up" | "down") => {
    setSaved(false);
    save.mutate(
      rating === "up"
        ? { rating, reason: "", wrongCitation: false }
        : { rating, reason: reason.trim(), wrongCitation: wrong },
    );
  };
  const submit = (e: FormEvent) => {
    e.preventDefault();
    rate("down");
  };
  const reasonId = useId();
  const wrongId = useId();
  return (
    <div className="flex flex-col gap-3 border-t border-rule pt-3">
      <fieldset className="flex flex-wrap items-center gap-2">
        <legend className="sr-only">{t("feedback.question")}</legend>
        <span aria-hidden className="text-base text-ink-muted">
          {t("feedback.question")}
        </span>
        <Button
          size="sm"
          variant={mark?.rating === "up" ? "primary" : "secondary"}
          aria-pressed={mark?.rating === "up"}
          disabled={save.isPending}
          onClick={() => rate("up")}
        >
          <ThumbsUp aria-hidden />
          {t("feedback.up")}
        </Button>
        <Button
          size="sm"
          variant={mark?.rating === "down" ? "primary" : "secondary"}
          aria-pressed={mark?.rating === "down"}
          disabled={save.isPending}
          onClick={() => rate("down")}
        >
          <ThumbsDown aria-hidden />
          {t("feedback.down")}
        </Button>
        {saved && !save.isPending && (
          <span role="status" className="text-sm text-ink-muted">
            {t("feedback.saved")}
          </span>
        )}
      </fieldset>
      {save.isError && (
        <Notice tone="danger">
          {save.error instanceof ApiError ? save.error.message : t("errors.network")}
        </Notice>
      )}
      {mark?.rating === "down" && (
        <form onSubmit={submit} className="flex flex-col gap-2">
          <label htmlFor={reasonId} className="label">
            {t("feedback.reason")}
          </label>
          <Textarea
            id={reasonId}
            aria-describedby={`${reasonId}-hint`}
            rows={2}
            maxLength={2000}
            value={reason}
            onChange={(e) => setReason(e.target.value)}
          />
          <p id={`${reasonId}-hint`} className="text-sm text-ink-muted">
            {t("feedback.reasonHint")}
          </p>
          <div className="flex flex-wrap items-center justify-between gap-3">
            <label htmlFor={wrongId} className="flex items-center gap-2 text-base">
              <input
                id={wrongId}
                type="checkbox"
                checked={wrong}
                onChange={(e) => setWrong(e.target.checked)}
                className="size-4 accent-accent"
              />
              {t("feedback.wrongCitation")}
            </label>
            <Button type="submit" size="sm" disabled={save.isPending}>
              {t("feedback.save")}
            </Button>
          </div>
        </form>
      )}
      {privacy && <p className="text-sm text-ink-muted">{t("feedback.privacy")}</p>}
    </div>
  );
}

function AnswerSheet({
  live,
  title,
  onOpen,
  footer,
}: {
  live: Live;
  title: string;
  onOpen: (o: Opened) => void;
  footer?: React.ReactNode;
}) {
  const a = live.answer;
  return (
    <Sheet title={title} description={live.question}>
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
        {a && a.markdown !== "" && (
          <HowWeKnow answer={a} onOpen={onOpen} down={a.feedback?.rating === "down"} />
        )}
        {footer}
      </div>
    </Sheet>
  );
}

const done = (a: Answer): Live => ({ draft: "", pending: false, answer: a, question: a.question });

/** Ask: the home page. A question streams its answer; an ID opens its thread with a follow-up field. */
export function AskPage() {
  const { id } = useParams({ strict: false }) as { id?: string };
  const navigate = useNavigate();
  const qc = useQueryClient();
  const [question, setQuestion] = useState("");
  const [followUp, setFollowUp] = useState("");
  const [live, setLive] = useState<Live>();
  const [started, setStarted] = useState<number>();
  const [opened, setOpened] = useState<Opened>();
  const abort = useRef<AbortController>(null);
  const shownId = useRef(id);

  const stored = useQuery({
    queryKey: ["question", id],
    enabled: id !== undefined,
    queryFn: async () =>
      unwrap(await api.GET("/api/v1/questions/{id}", { params: { path: { id: Number(id) } } })),
  });
  const thread = stored.data;

  useEffect(() => () => abort.current?.abort(), []);
  // Another thread, or a new question, drops the answer in progress.
  useEffect(() => {
    if (shownId.current === id) return;
    shownId.current = id;
    abort.current?.abort();
    setLive(undefined);
    setFollowUp("");
  }, [id]);

  const setThread = (key: string, update: (th?: Thread) => Thread | undefined) =>
    qc.setQueryData<Thread>(["question", key], update);

  const ask = async (q: string, threadId?: number) => {
    abort.current?.abort();
    const ctl = new AbortController();
    abort.current = ctl;
    let state: Live = { draft: "", pending: true, question: q };
    const update = (next: Partial<Live>) => {
      state = { ...state, ...next };
      setLive(state);
    };
    update({});
    try {
      const r = await api.POST("/api/v1/ask", {
        body: { question: q, threadId },
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
      const a = state.answer;
      // A refused follow-up is not stored. It stays on the page with its notice.
      if (!a || a.questionId === 0) return;
      qc.invalidateQueries({ queryKey: ["questions"] });
      if (threadId !== undefined && a.threadId === threadId) {
        setThread(String(threadId), (th) => th && { ...th, turns: [...th.turns, a] });
        setFollowUp("");
        setLive(undefined);
        return;
      }
      if (threadId !== undefined) setStarted(a.threadId);
      setThread(String(a.threadId), () => ({ threadId: a.threadId, turns: [a] }));
      navigate({ to: "/questions/$id", params: { id: String(a.threadId) } });
    } catch {
      if (!ctl.signal.aborted) update({ error: true, pending: false });
    }
  };

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const q = question.trim();
    if (q) ask(q);
  };
  const submitFollowUp = (e: FormEvent) => {
    e.preventDefault();
    const q = followUp.trim();
    if (q && thread) ask(q, thread.threadId);
  };
  const saved = (questionId: number) => (f: Feedback) =>
    setThread(
      id ?? "",
      (th) =>
        th && {
          ...th,
          turns: th.turns.map((a) => (a.questionId === questionId ? { ...a, feedback: f } : a)),
        },
    );

  const turns = thread?.turns ?? [];
  const last = turns[turns.length - 1];
  const results =
    live?.results ?? (last ? { entities: [], results: last.results, degraded: false } : undefined);
  const pending = live?.pending ?? false;

  return (
    <div className="flex flex-col gap-6">
      <header className="flex flex-wrap items-end justify-between gap-3">
        <div className="flex flex-col gap-1">
          <h1 className="text-3xl font-semibold tracking-tight">{t("ask.title")}</h1>
          <p className="text-base text-ink-muted">{t("ask.body")}</p>
        </div>
        {id !== undefined && (
          <Button asChild>
            <Link to="/">
              <Plus aria-hidden />
              {t("ask.newQuestion")}
            </Link>
          </Button>
        )}
      </header>
      {id === undefined && (
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
          <Button type="submit" variant="primary" disabled={pending} className="h-11">
            <CornerDownLeft aria-hidden />
            {pending ? t("ask.asking") : t("ask.submit")}
          </Button>
        </form>
      )}
      <div className="grid items-start gap-6 lg:grid-cols-[minmax(0,3fr)_minmax(0,2fr)]">
        <div className="flex min-w-0 flex-col gap-6">
          {id !== undefined && stored.isPending && !live && (
            <SkeletonRows label={t("app.loading")} rows={4} />
          )}
          {id !== undefined && stored.isError && <Notice tone="danger">{t("app.notFoundBody")}</Notice>}
          {started !== undefined && thread?.threadId === started && <Notice>{t("ask.threadStarted")}</Notice>}
          {turns.map((a) => (
            <AnswerSheet
              key={a.questionId}
              live={done(a)}
              title={turns.length > 1 || live ? t("ask.turn", { n: a.turn }) : t("ask.answer")}
              onOpen={setOpened}
              footer={
                a.notice?.code === "hidden" ? undefined : (
                  <Marks
                    key={a.questionId}
                    answer={a}
                    privacy={a === last && !live}
                    onSaved={saved(a.questionId)}
                  />
                )
              }
            />
          ))}
          {live && (
            <AnswerSheet
              live={live}
              title={thread ? t("ask.turn", { n: turns.length + 1 }) : t("ask.answer")}
              onOpen={setOpened}
            />
          )}
          {thread && (
            <form onSubmit={submitFollowUp} className="flex flex-col gap-2">
              <div className="flex flex-col gap-2 sm:flex-row sm:items-end">
                <Field label={t("ask.followUpLabel")} hint={t("ask.followUpHint")} className="flex-1">
                  {(p) => (
                    <input
                      {...p}
                      type="search"
                      required
                      maxLength={1000}
                      autoComplete="off"
                      placeholder={t("ask.followUpPlaceholder")}
                      value={followUp}
                      onChange={(e) => setFollowUp(e.target.value)}
                      className="h-11 w-full rounded-sm border border-rule-strong bg-paper-raised px-3 text-lg text-ink placeholder:text-ink-faint transition-ink focus-visible:border-accent"
                    />
                  )}
                </Field>
                <Button type="submit" variant="primary" disabled={pending} className="h-11 sm:mb-6">
                  <CornerDownLeft aria-hidden />
                  {pending ? t("ask.asking") : t("ask.submit")}
                </Button>
              </div>
              {turns.length >= 6 && <p className="text-sm text-ink-muted">{t("ask.threadFull")}</p>}
            </form>
          )}
          {results && (
            <Sheet title={t("ask.results")}>
              <ResultList entities={results.entities} results={results.results} degraded={results.degraded} />
            </Sheet>
          )}
        </div>
        <History current={thread?.threadId} />
      </div>
      <Drawer opened={opened} onClose={() => setOpened(undefined)} />
    </div>
  );
}
