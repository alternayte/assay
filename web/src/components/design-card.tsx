import { Link } from "@tanstack/react-router";
import { Field, Input, Textarea } from "@/components/ui/field";
import { t } from "@/copy";
import { Diagram } from "@/routes/design";
import { EntityLink } from "@/routes/entity";

/** The card kinds that this file shows. */
export const designKinds = ["design_problem", "design_edit", "design_update"];

type Text = { markdown?: string; diagram?: string };
type DesignPayload = {
  entityKey?: string;
  slug?: string;
  title?: string;
  reason?: string;
  note?: string;
  current?: Text;
  proposed?: Text;
};
type RepoPayload = {
  repository?: string;
  name?: string;
  option?: string;
  candidate?: string;
  candidateName?: string;
  candidateType?: string;
  newName?: string;
  group?: string[];
  signals?: { owners?: string[]; prefix?: string; edges?: number; projects?: string[]; readme?: string };
};

/** The edits that a reviewer makes to a design or repo_system card before accept. */
export type CardEdits = { canonicalName?: string; text?: string; diagram?: string };

function TextBlock({ label, text, tone }: { label: string; text?: Text; tone: "old" | "new" }) {
  return (
    <div
      className={
        tone === "new"
          ? "flex min-w-0 flex-col gap-2 rounded-sm border-l-4 border-accent bg-accent-wash px-3 py-2"
          : "flex min-w-0 flex-col gap-2 rounded-sm border-l-4 border-rule-strong bg-paper px-3 py-2"
      }
    >
      <span className="label">{label}</span>
      <p className="whitespace-pre-wrap break-words text-sm">{text?.markdown || t("designCard.noText")}</p>
      {text?.diagram && <Diagram source={text.diagram} label={label} />}
    </div>
  );
}

/** The body of a design_problem, design_edit or design_update card. */
export function DesignCardBody({
  kind,
  payload,
  editing,
  edits,
  setEdits,
}: {
  kind: string;
  payload: unknown;
  editing: boolean;
  edits: CardEdits;
  setEdits: (e: CardEdits) => void;
}) {
  const p = (payload ?? {}) as DesignPayload;
  const start = kind === "design_problem" ? p.current : p.proposed;
  return (
    <div className="flex min-w-0 flex-col gap-3">
      {p.entityKey && (
        <p className="flex flex-wrap items-baseline gap-2">
          <span className="label">{t("designCard.section")}</span>
          <Link
            to="/e/$key/design"
            params={{ key: p.entityKey }}
            className="break-words text-accent underline-offset-2 hover:underline"
          >
            {p.title}
          </Link>
        </p>
      )}
      {p.reason && (
        <p className="break-words">
          <span className="label mr-2">{t("designCard.reason")}</span>
          {p.reason}
        </p>
      )}
      {p.note && (
        <p className="break-words text-sm text-ink-muted">
          <span className="label mr-2">{t("designCard.note")}</span>
          {p.note}
        </p>
      )}
      <div className={kind === "design_problem" ? "grid gap-2" : "grid gap-2 lg:grid-cols-2"}>
        <TextBlock
          label={kind === "design_update" ? t("designCard.pinned") : t("designCard.current")}
          text={p.current}
          tone="old"
        />
        {kind !== "design_problem" && (
          <TextBlock
            label={kind === "design_update" ? t("designCard.draft") : t("designCard.proposed")}
            text={p.proposed}
            tone="new"
          />
        )}
      </div>
      {kind === "design_problem" && !editing && (
        <p className="text-sm text-ink-muted">{t("designCard.problemHint")}</p>
      )}
      {editing && (
        <div className="flex flex-col gap-3 rounded-sm border border-accent bg-accent-wash px-3 py-2">
          <p className="text-sm">{t("designCard.editHint")}</p>
          <Field label={t("designCard.text")}>
            {(f) => (
              <Textarea
                {...f}
                autoFocus
                rows={6}
                maxLength={8000}
                value={edits.text ?? start?.markdown ?? ""}
                onChange={(e) => setEdits({ ...edits, text: e.target.value })}
              />
            )}
          </Field>
          <Field label={t("designCard.diagram")}>
            {(f) => (
              <Textarea
                {...f}
                rows={5}
                maxLength={8000}
                className="font-mono text-sm"
                value={edits.diagram ?? start?.diagram ?? ""}
                onChange={(e) => setEdits({ ...edits, diagram: e.target.value })}
              />
            )}
          </Field>
        </div>
      )}
    </div>
  );
}

/** The body of a repo_system card: the repository, the proposed group and the signals. */
export function RepoSystemBody({
  payload,
  editing,
  edits,
  setEdits,
}: {
  payload: unknown;
  editing: boolean;
  edits: CardEdits;
  setEdits: (e: CardEdits) => void;
}) {
  const p = (payload ?? {}) as RepoPayload;
  const s = p.signals ?? {};
  const rows: [string, string][] = [];
  if (s.owners?.length) rows.push([t("designCard.owners"), s.owners.join(", ")]);
  if (s.prefix) rows.push([t("designCard.prefix"), s.prefix]);
  if (s.edges) rows.push([t("designCard.edges"), String(s.edges)]);
  if (s.projects?.length) rows.push([t("designCard.projects"), s.projects.join(", ")]);
  if (s.readme) rows.push([t("designCard.readme"), s.readme]);
  return (
    <div className="flex min-w-0 flex-col gap-3">
      <p className="flex flex-wrap items-baseline gap-2">
        <span className="label">{t("designCard.repository")}</span>
        {p.repository && <EntityLink entityKey={p.repository}>{p.name || p.repository}</EntityLink>}
      </p>
      <p className="flex flex-wrap items-baseline gap-2 rounded-sm border-l-4 border-accent bg-accent-wash px-3 py-2">
        {p.option === "new" ? (
          <span>{t("designCard.newSystem", { name: p.newName ?? "", count: p.group?.length ?? 0 })}</span>
        ) : (
          <>
            <span>{p.candidateType === "service" ? t("designCard.implements") : t("designCard.partOf")}</span>
            {p.candidate && <EntityLink entityKey={p.candidate}>{p.candidateName || p.candidate}</EntityLink>}
          </>
        )}
      </p>
      {p.option === "new" && (p.group?.length ?? 0) > 0 && (
        <ul className="flex flex-col gap-1 text-sm">
          {p.group?.map((g) => (
            <li key={g}>
              <EntityLink entityKey={g}>{g}</EntityLink>
            </li>
          ))}
        </ul>
      )}
      {rows.length > 0 && (
        <dl className="grid gap-x-4 gap-y-1 text-sm sm:grid-cols-[max-content_1fr]">
          {rows.map(([k, v]) => (
            <div key={k} className="contents">
              <dt className="label pt-0.5">{k}</dt>
              <dd className="break-words">{v}</dd>
            </div>
          ))}
        </dl>
      )}
      {editing && p.option === "new" && (
        <div className="flex flex-col gap-3 rounded-sm border border-accent bg-accent-wash px-3 py-2">
          <Field label={t("designCard.systemName")}>
            {(f) => (
              <Input
                {...f}
                autoFocus
                maxLength={200}
                value={edits.canonicalName ?? p.newName ?? ""}
                onChange={(e) => setEdits({ ...edits, canonicalName: e.target.value })}
              />
            )}
          </Field>
        </div>
      )}
    </div>
  );
}
