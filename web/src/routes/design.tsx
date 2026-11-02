import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { FileText, Flag, Pencil, RefreshCw } from "lucide-react";
import { type FormEvent, useEffect, useId, useRef, useState } from "react";
import Markdown, { type Components } from "react-markdown";
import rehypeSanitize from "rehype-sanitize";
import { ApiError, api, type Schemas, unwrap } from "@/api/client";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent } from "@/components/ui/dialog";
import { Field, Textarea } from "@/components/ui/field";
import { Notice } from "@/components/ui/notice";
import { Empty, Sheet } from "@/components/ui/sheet";
import { SkeletonRows } from "@/components/ui/skeleton";
import { type CopyKey, t } from "@/copy";
import { cn } from "@/lib/cn";
import { formatAgo, formatTime } from "@/lib/format";
import { atLeast, useMe } from "@/lib/session";
import { Drawer, External, type Opened } from "@/routes/ask";
import { EntityLink } from "@/routes/entity";

type DesignPage = Schemas["DesignPage"];
type Section = Schemas["DesignPageSection"];
type Cite = Schemas["DesignCite"];

const messageOf = (e: unknown) => (e instanceof ApiError ? e.message : t("errors.network"));

/** The design page of an entity, or undefined while it loads. A 404 means that the entity has none. */
export function useDesign(key: string) {
  return useQuery({
    queryKey: ["design", key],
    queryFn: async () =>
      unwrap(await api.GET("/api/v1/entities/{key}/design", { params: { path: { key } } })),
    retry: (n, e) => !(e instanceof ApiError && e.status === 404) && n < 2,
  });
}

/** A design cite as a source that the drawer opens. */
function sourceOf(c: Cite): Opened {
  return {
    kind: "source",
    source: {
      id: c.id,
      itemId: c.itemId ?? 0,
      title: c.start
        ? t("design.lines", { path: c.path ?? c.title ?? "", start: c.start, end: c.end ?? c.start })
        : (c.title ?? ""),
      url: c.url ?? "",
      kind: c.code ? "code" : "file",
      sourceType: c.sourceType ?? "github",
      connectionName: "",
      updatedAt: null,
      headingPath: [],
      passageHashes: c.hash ? [c.hash] : [],
      text: "",
    },
  };
}

function CiteChip({ cite, onOpen }: { cite: Cite; onOpen: (o: Opened) => void }) {
  const tone = cite.stale
    ? "border-stale text-stale hover:bg-wash-stale"
    : cite.kind === "claim"
      ? "border-rule-strong text-ink-muted hover:bg-paper-sunk"
      : "border-accent text-accent hover:bg-accent-wash";
  const label = cite.kind === "claim" ? (cite.text ?? "") : cite.path || cite.title || "";
  const cls = cn(
    "mx-0.5 inline-flex h-5 items-center rounded-sm border px-1 align-baseline font-mono text-xs transition-ink",
    tone,
  );
  if (cite.kind === "claim") {
    return (
      <span className={cls} title={label} data-testid="design-cite">
        {cite.id}
      </span>
    );
  }
  return (
    <button
      type="button"
      className={cls}
      title={label}
      aria-label={t("design.cite", { id: cite.id, source: label })}
      onClick={() => onOpen(sourceOf(cite))}
      data-testid="design-cite"
    >
      {cite.id}
    </button>
  );
}

/** The text of a section. Markers such as [S1] become chips that open the passage. */
function SectionText({ section, onOpen }: { section: Section; onOpen: (o: Opened) => void }) {
  const md = section.markdown.replace(/\[([SC]\d+)\]/g, "[$1](#cite-$1)");
  const a: Components["a"] = ({ href, children }) => {
    const id = href?.startsWith("#cite-") ? href.slice(6) : undefined;
    const cite = section.cites.find((c) => c.id === id);
    if (!cite) return <External href={href}>{children}</External>;
    return <CiteChip cite={cite} onOpen={onOpen} />;
  };
  return (
    <div className="prose-register text-lg leading-7" data-testid="design-text">
      <Markdown rehypePlugins={[rehypeSanitize]} components={{ a }}>
        {md}
      </Markdown>
    </div>
  );
}

function tokenColors() {
  const css = getComputedStyle(document.documentElement);
  const v = (name: string) => css.getPropertyValue(name).trim();
  return {
    background: v("--paper-raised"),
    mainBkg: v("--paper-sunk"),
    primaryColor: v("--paper-sunk"),
    primaryTextColor: v("--ink"),
    primaryBorderColor: v("--rule-strong"),
    secondaryColor: v("--accent-wash"),
    tertiaryColor: v("--paper"),
    nodeBorder: v("--rule-strong"),
    lineColor: v("--accent"),
    textColor: v("--ink"),
    edgeLabelBackground: v("--paper-raised"),
    clusterBkg: v("--paper"),
    clusterBorder: v("--rule"),
  };
}

let renders = 0;

/** A Mermaid diagram. Mermaid loads on first use and draws with the colour tokens of the theme. */
export function Diagram({ source, label }: { source: string; label: string }) {
  const ref = useRef<HTMLDivElement>(null);
  const [failed, setFailed] = useState(false);
  const [theme, setTheme] = useState(document.documentElement.dataset.theme);
  useEffect(() => {
    const o = new MutationObserver(() => setTheme(document.documentElement.dataset.theme));
    o.observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });
    return () => o.disconnect();
  }, []);
  useEffect(() => {
    let live = true;
    (async () => {
      try {
        const { default: mermaid } = await import("mermaid");
        mermaid.initialize({
          startOnLoad: false,
          securityLevel: "strict",
          theme: "base",
          fontFamily: "IBM Plex Sans",
          themeVariables: tokenColors(),
          flowchart: { htmlLabels: false },
        });
        renders += 1;
        const { svg } = await mermaid.render(`design-diagram-${theme ?? "light"}-${renders}`, source);
        if (!live || !ref.current) return;
        ref.current.replaceChildren(new DOMParser().parseFromString(svg, "image/svg+xml").documentElement);
        setFailed(false);
      } catch {
        if (live) setFailed(true);
      }
    })();
    return () => {
      live = false;
    };
  }, [source, theme]);
  return (
    <figure className="flex flex-col gap-2">
      {failed ? (
        <Notice tone="danger">{t("design.diagramError")}</Notice>
      ) : (
        <div
          ref={ref}
          role="img"
          aria-label={label}
          data-testid="design-diagram"
          className="design-diagram overflow-x-auto rounded-sm border border-rule bg-paper px-3 py-3 [&_svg]:mx-auto [&_svg]:max-w-full"
        />
      )}
    </figure>
  );
}

function CiteList({ section, onOpen }: { section: Section; onOpen: (o: Opened) => void }) {
  if (section.cites.length === 0) return null;
  return (
    <details className="text-sm">
      <summary className="label cursor-pointer select-none py-1">
        {t("design.sources", { count: section.cites.length })}
      </summary>
      <ul className="mt-1 flex flex-col divide-y divide-rule">
        {section.cites.map((c) => (
          <li key={c.id} className="flex flex-wrap items-baseline gap-x-3 gap-y-1 py-1">
            <span className="w-8 shrink-0 font-mono text-xs text-ink-muted">{c.id}</span>
            {c.kind === "claim" ? (
              <span className="break-words">{c.text}</span>
            ) : (
              <button
                type="button"
                onClick={() => onOpen(sourceOf(c))}
                className="break-all text-left font-mono text-xs text-accent underline-offset-2 hover:underline"
              >
                {c.start
                  ? t("design.lines", { path: c.path ?? "", start: c.start, end: c.end ?? c.start })
                  : c.path || c.title}
              </button>
            )}
            {c.kind === "passage" && <Badge>{c.code ? t("design.code") : t("design.document")}</Badge>}
            {c.stale && <Badge tone="stale">{t("design.staleCite")}</Badge>}
          </li>
        ))}
      </ul>
    </details>
  );
}

function stateBadge(s: Section) {
  if (s.pinned) return <Badge tone="accent">{t("design.pinned")}</Badge>;
  if (s.state === "stale") return <Badge tone="stale">{t("design.stale")}</Badge>;
  if (s.state === "missing") return <Badge>{t("design.missing")}</Badge>;
  return null;
}

function SectionSheet({
  pageKey,
  section,
  onOpen,
  onChanged,
}: {
  pageKey: string;
  section: Section;
  onOpen: (o: Opened) => void;
  onChanged: () => void;
}) {
  const [editing, setEditing] = useState(false);
  const [reporting, setReporting] = useState(false);
  const [msg, setMsg] = useState<string>();
  const description = [
    section.kind === "component" && section.componentPath && section.componentPath !== "."
      ? section.componentPath
      : "",
    section.editedAt
      ? section.pinned
        ? t("design.editedBy", { when: formatAgo(section.editedAt) })
        : t("design.written", { when: formatAgo(section.editedAt), model: section.model || "—" })
      : "",
  ]
    .filter(Boolean)
    .join(" · ");
  return (
    <Sheet
      title={section.title}
      description={description || undefined}
      actions={
        <div className="flex flex-wrap items-center gap-2">
          {stateBadge(section)}
          {section.editable && (
            <>
              <Button size="sm" variant="ghost" onClick={() => setEditing(true)}>
                <Pencil aria-hidden />
                {t("design.edit")}
              </Button>
              <Button size="sm" variant="ghost" onClick={() => setReporting(true)}>
                <Flag aria-hidden />
                {t("design.report")}
              </Button>
            </>
          )}
        </div>
      }
    >
      <div
        className="flex flex-col gap-4"
        data-testid="design-section"
        data-slug={section.slug}
        data-state={section.state}
      >
        {msg && <Notice tone="success">{msg}</Notice>}
        {section.repositoryKey && (
          <p className="text-sm">
            <EntityLink entityKey={section.repositoryKey}>
              {t("design.openRepository", { name: section.title })}
            </EntityLink>
          </p>
        )}
        {section.markdown ? (
          <SectionText section={section} onOpen={onOpen} />
        ) : section.diagram ? null : (
          <p className="text-ink-muted">
            {section.state === "missing" ? t("design.notWritten") : t("design.empty")}
          </p>
        )}
        {section.diagram && (
          <Diagram source={section.diagram} label={t("design.diagramLabel", { title: section.title })} />
        )}
        {section.lint.length > 0 && (
          <Notice>
            {t("design.lint", { count: section.lint.length })} {section.lint.map((l) => l.message).join(" ")}
          </Notice>
        )}
        <CiteList section={section} onOpen={onOpen} />
      </div>
      {editing && (
        <EditDialog
          pageKey={pageKey}
          section={section}
          onClose={() => setEditing(false)}
          onDone={(text) => {
            setEditing(false);
            setMsg(text);
            onChanged();
          }}
        />
      )}
      {reporting && (
        <ReportDialog
          pageKey={pageKey}
          section={section}
          onClose={() => setReporting(false)}
          onDone={() => {
            setReporting(false);
            setMsg(t("design.reported"));
          }}
        />
      )}
    </Sheet>
  );
}

function useDebounced<T>(value: T, ms: number) {
  const [v, setV] = useState(value);
  useEffect(() => {
    const id = window.setTimeout(() => setV(value), ms);
    return () => window.clearTimeout(id);
  }, [value, ms]);
  return v;
}

function EditDialog({
  pageKey,
  section,
  onClose,
  onDone,
}: {
  pageKey: string;
  section: Section;
  onClose: () => void;
  onDone: (msg: string) => void;
}) {
  const [markdown, setMarkdown] = useState(section.markdown);
  const [diagram, setDiagram] = useState(section.diagram);
  const [note, setNote] = useState("");
  const preview = useDebounced(diagram, 400);
  const save = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.PUT("/api/v1/entities/{key}/design/sections/{slug}", {
          params: { path: { key: pageKey, slug: section.slug } },
          body: { markdown, diagram, note: note || undefined },
        }),
      ),
    onSuccess: (r) => onDone(r.version ? t("design.saved") : t("design.proposed")),
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    save.mutate();
  };
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent
        title={t("design.editTitle", { title: section.title })}
        description={t("design.editBody")}
        closeLabel={t("design.close")}
        className="max-w-3xl"
      >
        <form onSubmit={submit} className="flex flex-col gap-4">
          {save.isError && <Notice tone="danger">{messageOf(save.error)}</Notice>}
          <Field label={t("design.text")} hint={t("design.textHint")}>
            {(p) => (
              <Textarea
                {...p}
                rows={8}
                required
                maxLength={8000}
                value={markdown}
                onChange={(e) => setMarkdown(e.target.value)}
              />
            )}
          </Field>
          <Field label={t("design.diagram")} hint={t("design.diagramHint")}>
            {(p) => (
              <Textarea
                {...p}
                rows={8}
                maxLength={8000}
                className="font-mono text-sm [font-variant-ligatures:none]"
                value={diagram}
                onChange={(e) => setDiagram(e.target.value)}
              />
            )}
          </Field>
          {preview.trim() && <Diagram source={preview} label={t("design.preview")} />}
          <Field label={t("design.note")}>
            {(p) => (
              <Textarea
                {...p}
                rows={2}
                maxLength={1000}
                value={note}
                onChange={(e) => setNote(e.target.value)}
              />
            )}
          </Field>
          <div className="flex gap-3">
            <Button type="button" onClick={onClose}>
              {t("design.cancel")}
            </Button>
            <Button type="submit" variant="primary" disabled={save.isPending}>
              {t("design.save")}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function ReportDialog({
  pageKey,
  section,
  onClose,
  onDone,
}: {
  pageKey: string;
  section: Section;
  onClose: () => void;
  onDone: () => void;
}) {
  const [reason, setReason] = useState("");
  const id = useId();
  const send = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.POST("/api/v1/entities/{key}/design/sections/{slug}/problems", {
          params: { path: { key: pageKey, slug: section.slug } },
          body: { reason },
        }),
      ),
    onSuccess: onDone,
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (reason.trim()) send.mutate();
  };
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent
        title={t("design.reportTitle", { title: section.title })}
        description={t("design.reportBody")}
        closeLabel={t("design.close")}
      >
        <form onSubmit={submit} className="flex flex-col gap-4" aria-describedby={id}>
          {send.isError && <Notice tone="danger">{messageOf(send.error)}</Notice>}
          <Field label={t("design.reason")}>
            {(p) => (
              <Textarea
                {...p}
                rows={4}
                required
                maxLength={2000}
                autoFocus
                value={reason}
                onChange={(e) => setReason(e.target.value)}
              />
            )}
          </Field>
          <div className="flex gap-3">
            <Button type="button" onClick={onClose}>
              {t("design.cancel")}
            </Button>
            <Button type="submit" variant="primary" disabled={send.isPending || !reason.trim()}>
              {t("design.send")}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function RunLine({ page }: { page: DesignPage }) {
  const r = page.run;
  if (!r) return null;
  const when = r.finishedAt ?? r.startedAt;
  return (
    <div className="flex flex-col gap-2">
      <p className="font-mono text-xs text-ink-faint" data-testid="design-run" data-status={r.status}>
        {t(`design.run.${r.status}` as CopyKey, {
          when: formatTime(when),
          tokens: r.tokens.toLocaleString(),
        })}
      </p>
      {r.reason && (
        <div data-testid="design-run-reason">
          <Notice tone={r.status === "failed" ? "danger" : "info"}>{r.reason}</Notice>
        </div>
      )}
    </div>
  );
}

/** The design tab of an entity page. */
export function DesignView({ entityKey }: { entityKey: string }) {
  const me = useMe();
  const qc = useQueryClient();
  const design = useDesign(entityKey);
  const [opened, setOpened] = useState<Opened>();
  const compiled = useRef<string>(undefined);
  const refresh = () => qc.invalidateQueries({ queryKey: ["design", entityKey] });
  const compile = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.POST("/api/v1/entities/{key}/design/compile", { params: { path: { key: entityKey } } }),
      ),
    onSuccess: (p) => qc.setQueryData(["design", entityKey], p),
  });
  const regenerate = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.POST("/api/v1/entities/{key}/design/regenerate", { params: { path: { key: entityKey } } }),
      ),
    onSuccess: refresh,
  });
  const p = design.data;
  const overview = p?.sections.find((s) => s.slug === "overview");
  const compileMutate = compile.mutate;
  useEffect(() => {
    if (p?.level !== "system" || compiled.current === entityKey) return;
    if (overview && !overview.pinned && (overview.state === "missing" || overview.state === "stale")) {
      compiled.current = entityKey;
      compileMutate();
    }
  }, [p, overview, entityKey, compileMutate]);
  useEffect(() => {
    if (!p?.updating) return;
    const id = window.setInterval(refresh, 5000);
    return () => window.clearInterval(id);
  });

  if (design.isPending) return <SkeletonRows label={t("app.loading")} rows={8} />;
  if (design.isError || !p) {
    return (
      <Empty>
        {design.error instanceof ApiError && design.error.status === 404 ? t("design.none") : t("app.error")}
      </Empty>
    );
  }
  const reviewer = atLeast(me, "reviewer");
  return (
    <div className="flex flex-col gap-6" data-testid="design-page" data-level={p.level}>
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="flex max-w-prose flex-col gap-2">
          <p className="text-base text-ink-muted">
            {p.level === "system" ? t("design.systemBody") : t("design.repositoryBody")}
          </p>
          <RunLine page={p} />
          {p.updating && <Badge tone="accent">{t("design.updating")}</Badge>}
          {compile.isPending && <p className="text-sm text-ink-muted">{t("design.compiling")}</p>}
          {compile.isError && <Notice tone="danger">{messageOf(compile.error)}</Notice>}
        </div>
        <div className="flex flex-wrap gap-2">
          <Button asChild variant="ghost" size="sm">
            <a
              href={`/e/${encodeURIComponent(entityKey)}/design.md`}
              target="_blank"
              rel="noopener noreferrer"
            >
              <FileText aria-hidden />
              {t("design.markdown")}
            </a>
          </Button>
          {reviewer && (
            <Button size="sm" onClick={() => regenerate.mutate()} disabled={regenerate.isPending}>
              <RefreshCw aria-hidden />
              {regenerate.isSuccess && p.level === "repository" ? t("design.queued") : t("design.regenerate")}
            </Button>
          )}
        </div>
      </div>
      {regenerate.isError && <Notice tone="danger">{messageOf(regenerate.error)}</Notice>}
      {p.sections.map((s) => (
        <SectionSheet key={s.slug} pageKey={entityKey} section={s} onOpen={setOpened} onChanged={refresh} />
      ))}
      <Drawer opened={opened} onClose={() => setOpened(undefined)} />
    </div>
  );
}
