import { Link } from "@tanstack/react-router";
import { Popover as P } from "radix-ui";
import type { Schemas } from "@/api/client";
import { Badge } from "@/components/ui/badge";
import { type CopyKey, t } from "@/copy";

export type Term = Schemas["Term"];
export type Linked = Schemas["Linked"];
type TermMatch = Schemas["Match"];

/** The UTF-16 index of each UTF-8 byte offset of text. */
function byteIndex(text: string): (byte: number) => number {
  const starts: [number, number][] = [];
  let bytes = 0;
  for (let i = 0; i < text.length; ) {
    const cp = text.codePointAt(i) ?? 0;
    starts.push([bytes, i]);
    bytes += cp < 0x80 ? 1 : cp < 0x800 ? 2 : cp < 0x10000 ? 3 : 4;
    i += cp > 0xffff ? 2 : 1;
  }
  return (byte) => starts.find(([b]) => b >= byte)?.[1] ?? text.length;
}

/** Markdown with each term link as [form](#term-N). N indexes the matches. */
export function linkMarkdown(markdown: string, matches: TermMatch[]): string {
  if (matches.length === 0) return markdown;
  const at = byteIndex(markdown);
  let out = markdown;
  for (let n = matches.length - 1; n >= 0; n--) {
    const m = matches[n];
    if (!m) continue;
    const start = at(m.start);
    const end = at(m.end);
    out = `${out.slice(0, start)}[${out.slice(start, end)}](#term-${n})${out.slice(end)}`;
  }
  return out;
}

/** The match of a #term-N link, or undefined. */
export function termOf(href: string | undefined, linked?: Linked): Term[] | undefined {
  if (!linked || !href?.startsWith("#term-")) return undefined;
  const m = linked.links[Number(href.slice(6))];
  if (!m) return undefined;
  return m.terms.map((k) => linked.terms.find((x) => x.key === k)).filter((x): x is Term => !!x);
}

/** Splits the links of joined passages back to each passage. */
export function splitLinks(texts: string[], linked: Linked, sep: string): Linked[] {
  const enc = new TextEncoder();
  const sepLen = enc.encode(sep).length;
  let offset = 0;
  return texts.map((text) => {
    const len = enc.encode(text).length;
    const own = linked.links
      .filter((m) => m.start >= offset && m.end <= offset + len)
      .map((m) => ({ ...m, start: m.start - offset, end: m.end - offset }));
    offset += len + sepLen;
    return { links: own, terms: linked.terms };
  });
}

const statusTone = { approved: "supported", deprecated: "stale", draft: "neutral" } as const;

export function TermStatus({ status }: { status: string }) {
  if (!status) return null;
  return (
    <Badge tone={statusTone[status as keyof typeof statusTone] ?? "neutral"}>
      {t(`glossary.status.${status}` as CopyKey)}
    </Badge>
  );
}

/** A linked term form: a dotted underline that opens the term popover. */
export function TermLink({ terms, children }: { terms: Term[]; children: React.ReactNode }) {
  return (
    <P.Root>
      <P.Trigger asChild>
        <button
          type="button"
          className="cursor-help border-b border-dotted border-accent text-inherit transition-ink hover:bg-accent-wash"
          aria-label={t("glossary.popoverLabel", { name: terms.map((x) => x.name).join(", ") })}
        >
          {children}
        </button>
      </P.Trigger>
      <P.Portal>
        <P.Content
          sideOffset={6}
          collisionPadding={16}
          className="z-50 flex w-80 max-w-[calc(100vw-2rem)] flex-col gap-3 rounded-md border border-rule-strong bg-paper-raised p-3 shadow-sheet"
        >
          {terms.length > 1 && <p className="text-sm text-ink-muted">{t("glossary.homonym")}</p>}
          {terms.map((term) => (
            <div key={term.key} className="flex flex-col gap-1">
              <div className="flex flex-wrap items-center gap-2">
                <span className="font-semibold">{term.name}</span>
                <TermStatus status={term.status} />
                {term.domain && <span className="text-xs text-ink-faint">{term.domain}</span>}
              </div>
              <p className="text-sm">{term.definition || t("glossary.noDefinition")}</p>
              {term.owner && (
                <p className="text-xs text-ink-muted">{t("glossary.ownedBy", { owner: term.owner })}</p>
              )}
              <Link
                to="/glossary/$key"
                params={{ key: term.key }}
                className="text-sm text-accent underline-offset-2 hover:underline"
              >
                {t("glossary.openTerm")}
              </Link>
            </div>
          ))}
          <P.Arrow className="fill-rule-strong" />
        </P.Content>
      </P.Portal>
    </P.Root>
  );
}
