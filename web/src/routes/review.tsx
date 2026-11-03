import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, CheckCheck, Pencil, SkipForward, Undo2, X } from "lucide-react";
import { type FormEvent, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { ApiError, api, type Schemas, unwrap } from "@/api/client";
import type { paths } from "@/api/schema.gen";
import { DesignCardBody, designKinds, RepoSystemBody } from "@/components/design-card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent } from "@/components/ui/dialog";
import { Field, Input, Select, Textarea } from "@/components/ui/field";
import { Notice } from "@/components/ui/notice";
import { Empty } from "@/components/ui/sheet";
import { SkeletonRows } from "@/components/ui/skeleton";
import { type CopyKey, t } from "@/copy";
import { cn } from "@/lib/cn";
import { formatAgo } from "@/lib/format";
import { claimText } from "@/routes/ask";
import { ClaimStatus, EntityLink } from "@/routes/entity";

type Card = Schemas["Card"];
type Kind = NonNullable<NonNullable<paths["/api/v1/cards"]["get"]["parameters"]["query"]>["kind"]>;
type Band = "high" | "medium" | "low";
type Tab = "needs" | "auto" | "decided";
type Filter = { kind: string; band: string; connection: string; type: string; domain: string; q: string };
type Edits = {
  canonicalName?: string;
  definition?: string;
  members?: string[];
  text?: string;
  diagram?: string;
  winner?: number;
  summary?: string;
  type?: string;
};
/** What a reviewer gives for a card kind that cannot be accepted without it. */
type Answer = { winner?: number; summary?: string };
type EditPayload = { draft?: string; lint?: { rule: string; message: string }[] };

/** A decision that waits for the undo period before it goes to the server. */
type Pending = { card: Card; action: "accept" | "reject"; reason?: string; edits?: Edits; timer: number };

const UNDO_MS = 5000;
const kinds: Kind[] = [
  "correction",
  "finding",
  "merge",
  "mention_link",
  "unnamed",
  "new_term",
  "term_edit",
  "conflict",
  "needs_edit",
  "repo_system",
  "design_problem",
  "design_edit",
  "design_update",
];
const bandTone = { high: "supported", medium: "stale", low: "conflict" } as const;
const editable: string[] = ["merge", "unnamed", "new_term", "term_edit", "repo_system", ...designKinds];

type TextPair = { markdown?: string; diagram?: string };
type EditablePayload = {
  option?: string;
  newName?: string;
  phrase?: string;
  definition?: string;
  record?: { term?: { name?: string; definition?: string } };
  current?: TextPair;
  proposed?: TextPair;
  types?: string[];
};

/** Whether the reviewer can change a card before accept. A repo_system card for an existing system has nothing to change. */
function canEdit(card: Card) {
  const p = (card.payload ?? {}) as EditablePayload;
  return editable.includes(card.kind) && !(card.kind === "repo_system" && p.option !== "new");
}

/** The edit fields start from the values of the card, so that accept saves what the reviewer sees. */
function startEdits(card: Card): Edits {
  const p = (card.payload ?? {}) as EditablePayload;
  switch (card.kind) {
    case "term_edit":
      return { canonicalName: p.record?.term?.name, definition: p.record?.term?.definition };
    case "new_term":
      return { canonicalName: p.phrase, definition: p.definition };
    case "repo_system":
      return { canonicalName: p.newName };
    case "design_problem":
      return { text: p.current?.markdown ?? "", diagram: p.current?.diagram ?? "" };
    case "design_edit":
    case "design_update":
      return { text: p.proposed?.markdown ?? "", diagram: p.proposed?.diagram ?? "" };
  }
  return {};
}

const editPayload = (card: Card) => (card.payload ?? {}) as EditPayload;
const summaryOf = (card: Card, a: Answer) => a.summary ?? editPayload(card).draft ?? "";

/** The answer that accept sends for a conflict or needs_edit card, or undefined while it is missing. */
function answerFor(card: Card, a: Answer): Answer | undefined {
  if (card.kind === "conflict") return a.winner === undefined ? undefined : { winner: a.winner };
  if (card.kind === "needs_edit") {
    const summary = summaryOf(card, a).trim();
    return summary ? { summary } : undefined;
  }
  return {};
}

const kindLabel = (k: string) => t(`review.kinds.${k}` as CopyKey);
const bandLabel = (b?: string) => t(`review.bands.${b || "none"}` as CopyKey);
const errorText = (e: unknown) => (e instanceof ApiError ? e.message : t("errors.network"));

function BandMark({ band }: { band?: string }) {
  if (!band) return <Badge>{bandLabel()}</Badge>;
  return <Badge tone={bandTone[band as Band]}>{bandLabel(band)}</Badge>;
}

/** The decision side of a card: kind, band, precision, probability, impact and reasons. */
function Decision({ card }: { card: Card }) {
  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center gap-2">
        <Badge tone="accent">{kindLabel(card.kind)}</Badge>
        <BandMark band={card.band} />
      </div>
      <dl className="flex flex-col gap-1 font-mono text-xs text-ink-muted">
        <dd data-testid="precision">
          {card.precision
            ? t("review.precision", { value: card.precision.value.toFixed(2) })
            : t("review.precisionNone")}
          {card.decisionId !== undefined && !card.calibrated && ` · ${t("review.defaults")}`}
        </dd>
        {card.p !== undefined && <dd>{t("review.probability", { value: card.p.toFixed(2) })}</dd>}
        <dd>{t("review.impact", { count: card.impact })}</dd>
        {card.connections.length > 0 && <dd>{card.connections.map((c) => c.name).join(", ")}</dd>}
      </dl>
      {card.reasons.length > 0 && (
        <div className="flex flex-col gap-1">
          <span className="label">{t("review.reasons")}</span>
          <ul className="flex list-disc flex-col gap-0.5 pl-5 text-sm" data-testid="reasons">
            {card.reasons.map((r) => (
              <li key={r}>{r}</li>
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}

/** One subject of a card, as a column of the evidence side. */
function SubjectColumn({
  s,
  pick,
}: {
  s: Schemas["Subject"];
  pick?: { checked: boolean; onChange: (v: boolean) => void };
}) {
  return (
    <div
      className="flex min-w-0 flex-col gap-1 rounded-sm border border-rule bg-paper px-3 py-2"
      data-testid="member"
    >
      <div className="flex items-start gap-2">
        {pick && (
          <input
            type="checkbox"
            className="mt-1 size-4 accent-accent"
            checked={pick.checked}
            onChange={(e) => pick.onChange(e.target.checked)}
            aria-label={t("review.member", { name: s.name })}
          />
        )}
        <div className="min-w-0">
          <EntityLink entityKey={s.key}>{s.name}</EntityLink>
          <p className="font-mono text-xs break-all text-ink-faint">
            {t(`entityTypes.${s.type}` as CopyKey)} · {s.key}
          </p>
        </div>
      </div>
      {s.aliases.length > 0 && (
        <p className="text-sm text-ink-muted">
          <span className="label">{t("review.aliases")}</span> {s.aliases.join(", ")}
        </p>
      )}
      {s.sources.length > 0 && (
        <p className="text-sm text-ink-muted">
          <span className="label">{t("review.sources")}</span> {s.sources.join(", ")}
        </p>
      )}
    </div>
  );
}

const detailLabel: Record<string, CopyKey> = {
  problem: "report.problem",
  reason: "report.reason",
  note: "report.note",
  quote: "review.quote",
  occurrences: "review.occurrences",
};
const problemLabel: Record<string, CopyKey> = {
  wrong_value: "report.wrong_value",
  outdated: "report.outdated",
  not_here: "report.not_here",
};

/** The text fields of a payload, one level deep. Numbers are IDs that the claims above show. */
const hiddenDetails = ["draft", "visibilityKey", "entityKey"];

function Details({ payload }: { payload: unknown }) {
  if (!payload || typeof payload !== "object") return null;
  const p = payload as Record<string, unknown> & {
    mention?: { value?: string };
    claim?: { Quote?: string };
    occurrences?: { quote?: string }[];
  };
  const rows: [string, string][] = [];
  for (const [k, v] of Object.entries(p)) {
    if (typeof v !== "string" || v === "" || hiddenDetails.includes(k)) continue;
    rows.push([k, k === "problem" && problemLabel[v] ? t(problemLabel[v]) : v]);
  }
  if (p.mention?.value) rows.push(["mention", p.mention.value]);
  // A card that stands for many places shows how many, and the first three quotes.
  const occ = p.occurrences ?? [];
  if (occ.length > 1) {
    rows.push(["occurrences", t("review.occurrenceCount", { count: occ.length })]);
    for (const o of occ.slice(0, 3))
      if (o.quote && !rows.some(([, v]) => v === o.quote)) rows.push(["quote", o.quote]);
  } else if (p.claim?.Quote) rows.push(["quote", p.claim.Quote]);
  if (rows.length === 0) return null;
  return (
    <dl className="grid gap-x-4 gap-y-1 text-sm sm:grid-cols-[max-content_1fr]">
      {rows.map(([k, v]) => (
        <div key={`${k}:${v}`} className="contents">
          <dt className="label pt-0.5">{detailLabel[k] ? t(detailLabel[k]) : k}</dt>
          <dd className="break-words">{v}</dd>
        </div>
      ))}
    </dl>
  );
}

function ClaimBlock({
  claim,
  pick,
}: {
  claim: Schemas["Claim"];
  pick?: { name: string; checked: boolean; onChange: () => void };
}) {
  const text = claimText(claim);
  return (
    <div
      className={cn(
        "flex flex-col gap-1 rounded-sm border bg-paper px-3 py-2",
        pick?.checked ? "border-accent" : "border-rule",
      )}
      data-testid="claim"
    >
      <div className="flex flex-wrap items-baseline gap-2">
        {pick && (
          <input
            type="radio"
            name={pick.name}
            className="size-4 self-center accent-accent"
            checked={pick.checked}
            onChange={pick.onChange}
            aria-label={t("review.winner", { claim: text })}
          />
        )}
        <span className="break-words">{text}</span>
        <ClaimStatus status={claim.status} noted={claim.conflictNoted} />
      </div>
      {claim.evidence.map((e) => (
        <blockquote
          key={`${e.itemId}-${e.passageHash}-${e.stance}`}
          className="border-l-4 border-accent bg-accent-wash px-2 py-0.5 text-sm"
        >
          {e.quote}
          <span className="ml-2 font-mono text-xs text-ink-faint">{e.title}</span>
        </blockquote>
      ))}
    </div>
  );
}

/** The evidence side of a card: subjects side by side, claims, details and edits. */
function Evidence({
  card,
  editing,
  edits,
  setEdits,
  answer,
  setAnswer,
}: {
  card: Card;
  editing: boolean;
  edits: Edits;
  setEdits: (e: Edits) => void;
  answer: Answer;
  setAnswer: (a: Answer) => void;
}) {
  const subjects = card.kind === "merge" ? (card.members ?? card.subjects) : card.subjects;
  const members = edits.members ?? subjects.map((s) => s.key);
  if (designKinds.includes(card.kind)) {
    return (
      <DesignCardBody
        kind={card.kind}
        payload={card.payload}
        editing={editing}
        edits={edits}
        setEdits={setEdits}
      />
    );
  }
  if (card.kind === "repo_system") {
    return <RepoSystemBody payload={card.payload} editing={editing} edits={edits} setEdits={setEdits} />;
  }
  return (
    <div className="flex min-w-0 flex-col gap-3">
      {subjects.length > 0 && (
        <div
          className={cn(
            "grid gap-2",
            subjects.length > 1 && "sm:grid-cols-2",
            subjects.length > 2 && "lg:grid-cols-3",
          )}
        >
          {subjects.map((s) => (
            <SubjectColumn
              key={s.key}
              s={s}
              pick={
                editing && card.kind === "merge"
                  ? {
                      checked: members.includes(s.key),
                      onChange: (v) =>
                        setEdits({
                          ...edits,
                          members: v ? [...members, s.key] : members.filter((k) => k !== s.key),
                        }),
                    }
                  : undefined
              }
            />
          ))}
        </div>
      )}
      {card.kind === "conflict" && (card.claims ?? []).length > 0 && (
        <p className="text-sm">{t("review.winnerHint")}</p>
      )}
      {(card.claims ?? []).map((c) => (
        <ClaimBlock
          key={c.id}
          claim={c}
          pick={
            card.kind === "conflict"
              ? {
                  name: `winner-${card.id}`,
                  checked: answer.winner === c.id,
                  onChange: () => setAnswer({ ...answer, winner: c.id }),
                }
              : undefined
          }
        />
      ))}
      <Details payload={card.payload} />
      {card.kind === "needs_edit" && <SummaryEdit card={card} answer={answer} setAnswer={setAnswer} />}
      {editing && (
        <div className="flex flex-col gap-3 rounded-sm border border-accent bg-accent-wash px-3 py-2">
          <p className="text-sm">{t("review.editHint")}</p>
          <Field label={card.kind === "merge" ? t("review.canonical") : t("review.name")}>
            {(p) => (
              <Input
                {...p}
                autoFocus
                maxLength={200}
                value={edits.canonicalName ?? ""}
                onChange={(e) => setEdits({ ...edits, canonicalName: e.target.value })}
              />
            )}
          </Field>
          {card.kind === "unnamed" && ((card.payload ?? {}) as EditablePayload).types && (
            <Field label={t("review.type")}>
              {(p) => {
                const types = ((card.payload ?? {}) as EditablePayload).types ?? [];
                return (
                  <Select
                    {...p}
                    value={edits.type ?? types[0]}
                    onChange={(e) => setEdits({ ...edits, type: e.target.value })}
                  >
                    {types.map((ty) => (
                      <option key={ty} value={ty}>
                        {t(`entityTypes.${ty}` as CopyKey)}
                      </option>
                    ))}
                  </Select>
                );
              }}
            </Field>
          )}
          {(card.kind === "new_term" || card.kind === "term_edit") && (
            <Field label={t("review.definition")}>
              {(p) => (
                <Textarea
                  {...p}
                  rows={3}
                  maxLength={4000}
                  value={edits.definition ?? ""}
                  onChange={(e) => setEdits({ ...edits, definition: e.target.value })}
                />
              )}
            </Field>
          )}
        </div>
      )}
    </div>
  );
}

/** The corrected summary of a needs_edit card, with the checks that the draft failed. */
function SummaryEdit({
  card,
  answer,
  setAnswer,
}: {
  card: Card;
  answer: Answer;
  setAnswer: (a: Answer) => void;
}) {
  const lint = editPayload(card).lint ?? [];
  return (
    <div className="flex flex-col gap-3 rounded-sm border border-accent bg-accent-wash px-3 py-2">
      {lint.length > 0 && (
        <div className="flex flex-col gap-1">
          <span className="label">{t("review.failedChecks")}</span>
          <ul className="flex list-disc flex-col gap-0.5 pl-5 text-sm" data-testid="lint">
            {lint.map((l) => (
              <li key={`${l.rule}-${l.message}`}>{l.message}</li>
            ))}
          </ul>
        </div>
      )}
      <Field label={t("review.summary")} hint={t("review.summaryHint")}>
        {(p) => (
          <Textarea
            {...p}
            rows={6}
            maxLength={4000}
            value={summaryOf(card, answer)}
            onChange={(e) => setAnswer({ ...answer, summary: e.target.value })}
          />
        )}
      </Field>
    </div>
  );
}

function ReasonDialog({ onSend, onClose }: { onSend: (reason: string) => void; onClose: () => void }) {
  const [reason, setReason] = useState("");
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (reason.trim()) onSend(reason.trim());
  };
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent title={t("review.reasonTitle")} closeLabel={t("review.close")}>
        <form onSubmit={submit} className="flex flex-col gap-4">
          <Field label={t("review.reasonLabel")} hint={t("review.reasonHint")}>
            {(p) => (
              <Textarea
                {...p}
                autoFocus
                rows={3}
                maxLength={1000}
                value={reason}
                onChange={(e) => setReason(e.target.value)}
              />
            )}
          </Field>
          <div className="flex gap-3">
            <Button onClick={onClose}>{t("review.cancel")}</Button>
            <Button type="submit" variant="primary" disabled={!reason.trim()}>
              {t("review.send")}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function Filters({
  f,
  set,
  options,
  domains,
  types,
}: {
  f: Filter;
  set: (f: Filter) => void;
  options: Schemas["CardConnection"][];
  domains: { id: number; name: string }[];
  types: string[];
}) {
  const sel = (key: keyof Filter, label: string, opts: [string, string][]) => (
    <Field label={label}>
      {(p) => (
        <Select {...p} value={f[key]} onChange={(e) => set({ ...f, [key]: e.target.value })}>
          <option value="">{t("review.all")}</option>
          {opts.map(([v, l]) => (
            <option key={v} value={v}>
              {l}
            </option>
          ))}
        </Select>
      )}
    </Field>
  );
  return (
    <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-[minmax(0,2fr)_repeat(5,minmax(0,1fr))]">
      <Field label={t("review.search")} className="col-span-2 sm:col-span-3 lg:col-span-1">
        {(p) => <Input {...p} type="search" value={f.q} onChange={(e) => set({ ...f, q: e.target.value })} />}
      </Field>
      {sel(
        "kind",
        t("review.kind"),
        kinds.map((k) => [k, kindLabel(k)]),
      )}
      {sel(
        "band",
        t("review.band"),
        (["high", "medium", "low"] as const).map((b) => [b, bandLabel(b)]),
      )}
      {sel(
        "connection",
        t("review.connection"),
        options.map((c) => [String(c.id), c.name]),
      )}
      {sel(
        "type",
        t("review.type"),
        types.map((x) => [x, t(`entityTypes.${x}` as CopyKey)]),
      )}
      {sel(
        "domain",
        t("review.domain"),
        domains.map((d) => [String(d.id), d.name]),
      )}
    </div>
  );
}

const query = (f: Filter) => ({
  kind: (f.kind || undefined) as Kind | undefined,
  band: (f.band || undefined) as Band | undefined,
  connection: f.connection ? Number(f.connection) : undefined,
  type: f.type || undefined,
  domain: f.domain ? Number(f.domain) : undefined,
  q: f.q.trim() || undefined,
});

function Needs({ f }: { f: Filter }) {
  const qc = useQueryClient();
  const cards = useQuery({
    queryKey: ["cards", "open", f],
    queryFn: async () =>
      unwrap(
        await api.GET("/api/v1/cards", { params: { query: { ...query(f), status: "open", limit: 200 } } }),
      ).cards,
  });
  const [skipped, setSkipped] = useState<number[]>([]);
  const [pending, setPending] = useState<Pending[]>([]);
  const [current, setCurrent] = useState<number>();
  // The position of the last decided or skipped card: the queue shows the card there when the next card is gone.
  const [pos, setPos] = useState(0);
  const [editing, setEditing] = useState(false);
  const [edits, setEdits] = useState<Edits>({});
  // Answers stay per card until the server accepts them, so a refused summary comes back for the next try.
  const [answers, setAnswers] = useState<Record<number, Answer>>({});
  const [asking, setAsking] = useState<"reject" | "needs">();
  const [bulk, setBulk] = useState(false);
  const [msg, setMsg] = useState<{ tone: "success" | "danger" | "info"; text: string }>();
  const pendingRef = useRef(pending);
  pendingRef.current = pending;

  const all = cards.data ?? [];
  const waiting = all.filter((c) => !pending.some((p) => p.card.id === c.id));
  const visible = [
    ...waiting.filter((c) => !skipped.includes(c.id)),
    ...skipped.flatMap((id) => waiting.filter((c) => c.id === id)),
  ];
  const card = visible.find((c) => c.id === current) ?? visible[Math.min(pos, visible.length - 1)];
  /** Moves to the card after the current one, as the queue shows it now. */
  const advance = () => {
    const i = card ? visible.indexOf(card) : 0;
    setCurrent(visible[i + 1]?.id);
    setPos(i);
  };

  const send = useCallback(
    async (p: Pending) => {
      const path = { params: { path: { id: p.card.id } } };
      try {
        if (p.action === "accept") {
          unwrap(await api.POST("/api/v1/cards/{id}/accept", { ...path, body: p.edits ?? {} }));
        } else {
          unwrap(await api.POST("/api/v1/cards/{id}/reject", { ...path, body: { reason: p.reason ?? "" } }));
        }
        setAnswers(({ [p.card.id]: _, ...rest }) => rest);
      } catch (e) {
        setMsg({ tone: "danger", text: t("review.failed", { message: errorText(e) }) });
      }
      // The card stays out of the queue until the reload no longer has it.
      await Promise.all([
        qc.invalidateQueries({ queryKey: ["cards"] }),
        qc.invalidateQueries({ queryKey: ["decisions"] }),
      ]);
      setPending((ps) => ps.filter((x) => x.card.id !== p.card.id));
    },
    [qc],
  );

  // Pending decisions go to the server when the reviewer leaves the tab.
  useEffect(
    () => () => {
      for (const p of pendingRef.current) {
        clearTimeout(p.timer);
        send(p);
      }
    },
    [send],
  );

  const answer = card ? (answers[card.id] ?? {}) : {};
  const ready = card ? answerFor(card, answer) : undefined;
  const setAnswer = (a: Answer) => card && setAnswers((as) => ({ ...as, [card.id]: a }));
  const decide = (action: "accept" | "reject", reason?: string) => {
    if (!card) return;
    if (action === "accept" && !ready) return;
    const e = action === "accept" ? { ...(editing ? edits : {}), ...ready } : undefined;
    const p: Pending = { card, action, reason, edits: e, timer: 0 };
    p.timer = window.setTimeout(() => send(p), UNDO_MS);
    advance();
    setPending((ps) => [...ps, p]);
    setEditing(false);
    setEdits({});
    setMsg({
      tone: "info",
      text: t("review.pending", {
        action: action === "accept" ? t("review.statuses.accepted") : t("review.statuses.rejected"),
      }),
    });
  };
  const needsEvidence = useMutation({
    mutationFn: async ({ id, reason }: { id: number; reason: string }) =>
      unwrap(
        await api.POST("/api/v1/cards/{id}/needs-evidence", { params: { path: { id } }, body: { reason } }),
      ),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["cards"] }),
    onError: (e) => setMsg({ tone: "danger", text: t("review.failed", { message: errorText(e) }) }),
  });
  const undo = () => {
    const last = pending.at(-1);
    if (!last) return;
    clearTimeout(last.timer);
    setPending((ps) => ps.slice(0, -1));
    setCurrent(last.card.id);
    setMsg({ tone: "info", text: t("review.undone") });
  };
  const skip = () => {
    if (!card) return;
    advance();
    setSkipped((s) => [...s.filter((id) => id !== card.id), card.id]);
    setEditing(false);
  };
  const edit = () => {
    if (!card || !canEdit(card)) return;
    setEdits(startEdits(card));
    setEditing(true);
  };

  const keys = useRef<(e: KeyboardEvent) => void>(undefined);
  keys.current = (e: KeyboardEvent) => {
    const target = e.target as HTMLElement;
    if (e.metaKey || e.ctrlKey || e.altKey || asking || bulk) return;
    // A picked radio or checkbox keeps the focus; the keys still work there, but not in a text field.
    if (target.closest("input:not([type=radio], [type=checkbox]), textarea, select, [role=dialog]")) return;
    const k = e.key.toLowerCase();
    if (k === "a") decide("accept");
    else if (k === "r") setAsking("reject");
    else if (k === "s") skip();
    else if (k === "e") edit();
    else if (k === "u") undo();
    else return;
    e.preventDefault();
  };
  useEffect(() => {
    const h = (e: KeyboardEvent) => keys.current?.(e);
    window.addEventListener("keydown", h);
    return () => window.removeEventListener("keydown", h);
  }, []);

  const inBand = f.band ? waiting.filter((c) => c.band === f.band) : [];
  const accepted = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.POST("/api/v1/cards/accept-band", {
          body: { ...query(f), band: f.band as Band },
        }),
      ).accepted,
    onSuccess: (ids) => {
      setBulk(false);
      setMsg({ tone: "success", text: t("review.bulkDone", { count: ids.length }) });
      qc.invalidateQueries({ queryKey: ["cards"] });
    },
  });

  if (cards.isPending) return <SkeletonRows label={t("app.loading")} rows={6} />;
  if (cards.isError) return <Notice tone="danger">{errorText(cards.error)}</Notice>;
  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="font-mono text-xs text-ink-muted">{t("review.keys")}</p>
        {f.band && inBand.length > 0 && (
          <Button variant="primary" size="sm" onClick={() => setBulk(true)}>
            <CheckCheck aria-hidden />
            {t("review.bulk", { band: bandLabel(f.band) })}
          </Button>
        )}
      </div>
      {msg && (
        <div className="flex flex-wrap items-center gap-3">
          <Notice tone={msg.tone} className="flex-1">
            {msg.text}
          </Notice>
          {pending.length > 0 && (
            <Button size="sm" onClick={undo}>
              <Undo2 aria-hidden />
              {t("review.undo")}
            </Button>
          )}
        </div>
      )}
      {!card ? (
        <Empty>{t("review.empty")}</Empty>
      ) : (
        <>
          <article
            aria-label={t("review.position", { n: visible.indexOf(card) + 1, total: visible.length })}
            data-testid="card"
            data-card={card.id}
            className="grid gap-4 rounded-md border border-rule-strong bg-paper-raised p-4 shadow-sheet md:grid-cols-[minmax(12rem,1fr)_3fr]"
          >
            <Decision card={card} />
            <Evidence
              card={card}
              editing={editing}
              edits={edits}
              setEdits={setEdits}
              answer={answer}
              setAnswer={setAnswer}
            />
            <div className="flex flex-wrap gap-2 md:col-span-2">
              <Button variant="primary" onClick={() => decide("accept")} disabled={!ready}>
                <Check aria-hidden />
                {t("review.accept")}
                <kbd className="font-mono text-xs opacity-70">A</kbd>
              </Button>
              <Button onClick={() => setAsking("reject")}>
                <X aria-hidden />
                {t("review.reject")}
                <kbd className="font-mono text-xs opacity-70">R</kbd>
              </Button>
              {(card.kind === "correction" || card.kind === "finding") && (
                <Button onClick={() => setAsking("needs")}>{t("review.needsEvidence")}</Button>
              )}
              <Button variant="ghost" onClick={skip}>
                <SkipForward aria-hidden />
                {t("review.skip")}
                <kbd className="font-mono text-xs opacity-70">S</kbd>
              </Button>
              {canEdit(card) && (
                <Button variant="ghost" onClick={edit} disabled={editing}>
                  <Pencil aria-hidden />
                  {t("review.edit")}
                  <kbd className="font-mono text-xs opacity-70">E</kbd>
                </Button>
              )}
            </div>
          </article>
          <Queue cards={visible} current={card.id} onPick={(id) => setCurrent(id)} />
        </>
      )}
      {asking && card && (
        <ReasonDialog
          onClose={() => setAsking(undefined)}
          onSend={(reason) => {
            setAsking(undefined);
            if (asking === "reject") decide("reject", reason);
            else needsEvidence.mutate({ id: card.id, reason });
          }}
        />
      )}
      {bulk && (
        <Dialog open onOpenChange={(o) => !o && setBulk(false)}>
          <DialogContent
            title={t("review.bulkTitle", { count: inBand.length, band: bandLabel(f.band) })}
            description={t("review.bulkBody")}
            closeLabel={t("review.close")}
          >
            {accepted.isError && <Notice tone="danger">{errorText(accepted.error)}</Notice>}
            <div className="flex gap-3">
              <Button onClick={() => setBulk(false)}>{t("review.cancel")}</Button>
              <Button variant="primary" disabled={accepted.isPending} onClick={() => accepted.mutate()}>
                {t("review.bulk", { band: bandLabel(f.band) })}
              </Button>
            </div>
          </DialogContent>
        </Dialog>
      )}
    </div>
  );
}

/** The cards after the current one, in queue order. */
function Queue({ cards, current, onPick }: { cards: Card[]; current: number; onPick: (id: number) => void }) {
  const rest = cards.filter((c) => c.id !== current).slice(0, 20);
  if (rest.length === 0) return null;
  return (
    <ol className="flex flex-col divide-y divide-rule rounded-sm border border-rule">
      {rest.map((c) => (
        <li key={c.id}>
          <button
            type="button"
            onClick={() => onPick(c.id)}
            className="flex w-full flex-wrap items-center gap-x-3 gap-y-1 px-3 py-1.5 text-left hover:bg-paper-sunk"
          >
            <span className="w-28 shrink-0 text-sm text-ink-muted">{kindLabel(c.kind)}</span>
            <BandMark band={c.band} />
            <span className="min-w-0 flex-1 truncate">
              {c.subjects.map((s) => s.name).join(" · ") || c.target}
            </span>
            <span className="font-mono text-xs text-ink-faint">{formatAgo(c.createdAt)}</span>
          </button>
        </li>
      ))}
    </ol>
  );
}

function Auto() {
  const qc = useQueryClient();
  const list = useQuery({
    queryKey: ["decisions", "auto"],
    queryFn: async () =>
      unwrap(await api.GET("/api/v1/decisions", { params: { query: { outcome: "auto" } } })).decisions,
  });
  const [msg, setMsg] = useState<string>();
  const undo = useMutation({
    mutationFn: async (id: number) =>
      unwrap(await api.POST("/api/v1/decisions/{id}/undo", { params: { path: { id } } })),
    onSuccess: () => {
      setMsg(t("review.autoUndone"));
      qc.invalidateQueries({ queryKey: ["decisions"] });
    },
  });
  if (list.isPending) return <SkeletonRows label={t("app.loading")} rows={4} />;
  if (list.isError) return <Notice tone="danger">{errorText(list.error)}</Notice>;
  if (list.data.length === 0) return <Empty>{t("review.emptyAuto")}</Empty>;
  return (
    <div className="flex flex-col gap-3">
      {msg && <Notice tone="success">{msg}</Notice>}
      {undo.isError && <Notice tone="danger">{errorText(undo.error)}</Notice>}
      <ul className="flex flex-col divide-y divide-rule">
        {list.data.map((d) => (
          <li key={d.id} className="flex flex-wrap items-center gap-x-3 gap-y-1 py-2" data-testid="auto">
            <Badge tone="accent">{d.kind.replaceAll("_", " ")}</Badge>
            <BandMark band={d.band} />
            <span className="min-w-0 flex-1">{d.subjects.map((s) => s.name).join(" · ")}</span>
            <span className="font-mono text-xs text-ink-faint">{formatAgo(d.at)}</span>
            <Button size="sm" variant="danger" disabled={undo.isPending} onClick={() => undo.mutate(d.id)}>
              <Undo2 aria-hidden />
              {t("review.undoAuto")}
            </Button>
          </li>
        ))}
      </ul>
    </div>
  );
}

const statusTone = {
  accepted: "supported",
  rejected: "danger",
  needs_evidence: "stale",
  closed: "neutral",
  open: "neutral",
} as const;

function Decided({ f }: { f: Filter }) {
  const list = useQuery({
    queryKey: ["cards", "decided", f],
    queryFn: async () =>
      unwrap(
        await api.GET("/api/v1/cards", { params: { query: { ...query(f), status: "decided", limit: 200 } } }),
      ).cards,
  });
  if (list.isPending) return <SkeletonRows label={t("app.loading")} rows={4} />;
  if (list.isError) return <Notice tone="danger">{errorText(list.error)}</Notice>;
  if (list.data.length === 0) return <Empty>{t("review.emptyDecided")}</Empty>;
  return (
    <ul className="flex flex-col divide-y divide-rule">
      {list.data.map((c) => (
        <li key={c.id} className="flex flex-col gap-1 py-2" data-testid="decided">
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
            <span className="w-28 shrink-0 text-sm text-ink-muted">{kindLabel(c.kind)}</span>
            <Badge tone={statusTone[c.status]}>{t(`review.statuses.${c.status}` as CopyKey)}</Badge>
            <BandMark band={c.band} />
            <span className="min-w-0 flex-1">{c.subjects.map((s) => s.name).join(" · ") || c.target}</span>
            {c.decidedAt && (
              <span className="font-mono text-xs text-ink-faint">{formatAgo(c.decidedAt)}</span>
            )}
          </div>
          {(c.reviewer || c.reason) && (
            <p className="text-sm text-ink-muted">
              {c.reviewer &&
                t("review.decidedBy", {
                  status: t(`review.statuses.${c.status}` as CopyKey),
                  who: c.reviewer,
                })}
              {c.reason && ` · ${c.reason}`}
            </p>
          )}
        </li>
      ))}
    </ul>
  );
}

const empty: Filter = { kind: "", band: "", connection: "", type: "", domain: "", q: "" };

/** Review: the queue of cards, the auto-applied decisions and the decided cards. */
export function ReviewPage() {
  const [tab, setTab] = useState<Tab>("needs");
  const [f, setF] = useState<Filter>(empty);
  const options = useQuery({
    queryKey: ["cards", "options"],
    queryFn: async () =>
      unwrap(await api.GET("/api/v1/cards", { params: { query: { status: "open", limit: 500 } } })).cards,
  });
  const domains = useQuery({
    queryKey: ["domains"],
    queryFn: async () => unwrap(await api.GET("/api/v1/domains")).domains,
  });
  const registry = useQuery({
    queryKey: ["predicates"],
    queryFn: async () => unwrap(await api.GET("/api/v1/predicates")),
  });
  const conns = useMemo(() => {
    const m = new Map<number, Schemas["CardConnection"]>();
    for (const c of options.data ?? []) for (const k of c.connections) m.set(k.id, k);
    return [...m.values()].sort((a, b) => a.name.localeCompare(b.name));
  }, [options.data]);
  const tabs: [Tab, string][] = [
    ["needs", t("review.tabs.needs")],
    ["auto", t("review.tabs.auto")],
    ["decided", t("review.tabs.decided")],
  ];
  return (
    <div className="flex flex-col gap-6">
      <header className="flex flex-col gap-1 border-b border-rule-strong pb-4">
        <h1 className="text-3xl font-semibold tracking-tight">{t("review.title")}</h1>
        <p className="text-base text-ink-muted">{t("review.body")}</p>
      </header>
      <div role="tablist" aria-label={t("review.title")} className="flex gap-1 border-b border-rule">
        {tabs.map(([id, label]) => (
          <button
            key={id}
            type="button"
            role="tab"
            id={`tab-${id}`}
            aria-selected={tab === id}
            aria-controls={`panel-${id}`}
            onClick={() => setTab(id)}
            className={cn(
              "-mb-px border-b-2 px-3 py-2 text-base transition-ink",
              tab === id
                ? "border-accent font-medium text-ink"
                : "border-transparent text-ink-muted hover:text-ink",
            )}
          >
            {label}
          </button>
        ))}
      </div>
      {tab !== "auto" && (
        <Filters
          f={f}
          set={setF}
          options={conns}
          domains={domains.data ?? []}
          types={registry.data?.entityTypes ?? []}
        />
      )}
      <section role="tabpanel" id={`panel-${tab}`} aria-labelledby={`tab-${tab}`}>
        {/* The queue keeps its state when the filter changes, so typing does not send the pending decisions. */}
        {tab === "needs" && <Needs f={f} />}
        {tab === "auto" && <Auto />}
        {tab === "decided" && <Decided f={f} />}
      </section>
    </div>
  );
}
