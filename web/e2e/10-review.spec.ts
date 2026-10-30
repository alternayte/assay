import { expect, type Page, test } from "@playwright/test";
import {
  appendToPage,
  call,
  connectionId,
  fakeAnswerModel,
  fixtureMethods,
  github,
  member,
  owner,
  restricted,
  reviewer,
  signIn,
  sql,
  syncNow,
} from "./env";

const paymentsAPI = "backstage:component:default/payments-api";
const remindersTeam = "backstage:group:default/reminders-team";
const pageURL = (key: string) => `/e/${encodeURIComponent(key)}`;

async function nameOf(page: Page, key: string): Promise<string> {
  const r = await call(page, "GET", `/api/v1/entities/${encodeURIComponent(key)}`);
  expect(r.status).toBe(200);
  return r.json.name;
}

/** Adds a service entity with one name, sourced from a connection. */
function seedService(key: string, name: string, connection = "acme-github") {
  sql(`
WITH e AS (INSERT INTO entities (type, key) VALUES ('service', '${key}') ON CONFLICT (key) DO NOTHING RETURNING id),
     k AS (SELECT id FROM connections WHERE name = '${connection}'),
     s AS (INSERT INTO entity_sources (entity_id, connection_id) SELECT e.id, k.id FROM e, k)
INSERT INTO entity_aliases (entity_id, connection_id, alias, alias_norm, is_name)
SELECT e.id, k.id, '${name}', lower('${name}'), true FROM e, k;`);
}

/** Adds a same_entity decision with a merge card. */
function seedPair(a: string, b: string, p: number, band: string, reasons: string[], outcome = "open") {
  const card =
    outcome === "open"
      ? `INSERT INTO proposals (kind, target, payload, via, connections, decision_id)
         SELECT 'merge', 'entity:${a}', '{}', 'system', ARRAY[k.id], d.id FROM d, k;`
      : "SELECT 1;";
  sql(`
WITH k AS (SELECT id FROM connections WHERE name = 'acme-github'),
     d AS (INSERT INTO decisions (kind, subject_refs, input_hash, probs, raw_p, band, connections, outcome, reasons, reviewed_at)
           SELECT 'same_entity', ARRAY['${a}', '${b}'], sha256(random()::text::bytea), '{}', ${p}, '${band}', ARRAY[k.id],
                  '${outcome}', '${JSON.stringify(reasons)}'::jsonb, CASE WHEN '${outcome}' = 'accepted' THEN now() END FROM k
           RETURNING id)
${card}`);
}

/** The open cards of the reviewer by kind, through the API. */
async function openCards(page: Page, kind: string) {
  const r = await call(page, "GET", `/api/v1/cards?kind=${kind}`);
  return r.json.cards as { id: number; subjects: { key: string }[] }[];
}

/** Moves the focus off the filter fields, so that the queue keys work. */
async function focusQueue(page: Page) {
  await page.getByRole("heading", { name: "Review", level: 1 }).click();
}

/** Waits for the decision that the undo period holds back. */
function decided(page: Page, action: "accept" | "reject") {
  return page.waitForResponse((r) => r.url().includes(`/${action}`) && r.request().method() === "POST", {
    timeout: 15_000,
  });
}

test("SCN-REV-001 a member reports a wrong owner with a source; after accept, page and answer show the new owner", async ({
  page,
}) => {
  await signIn(page, owner);
  await page.goto("/");
  await github(page);
  const team = await nameOf(page, remindersTeam);

  await signIn(page, member);
  await page.goto(pageURL(paymentsAPI));
  const fact = page.getByTestId("fact").filter({ hasText: /owned by/i });
  await fact.getByRole("button").last().click();
  const drawer = page.getByRole("dialog");
  await drawer.getByRole("button", { name: "Report a problem" }).click();
  const report = page.getByRole("dialog", { name: "Report a problem" });
  await report.getByLabel("Problem").selectOption("wrong_value");
  await report.getByLabel("Reason").fill("The reminders team owns the service since the move.");
  await report.getByLabel("Correct value").fill("remind");
  await report.locator("button", { hasText: remindersTeam }).click();
  await report.getByLabel("Source").fill("Team chart of the payments area.");
  await report.getByRole("button", { name: "Send report" }).click();
  await expect(drawer.getByText("Your report is in the review queue.")).toBeVisible();

  await signIn(page, reviewer);
  await page.goto("/review");
  await page.getByLabel("Kind").selectOption("correction");
  const card = page.getByTestId("card");
  await expect(card).toContainText("Correction");
  await expect(card).toContainText(team);
  const accept = decided(page, "accept");
  await focusQueue(page);
  await page.keyboard.press("a");
  expect((await accept).status()).toBe(204);

  await page.goto(pageURL(paymentsAPI));
  await expect(page.getByText("Owner", { exact: true }).locator("..")).toContainText(team);
  await expect(
    page
      .getByTestId("fact")
      .filter({ hasText: /owned by/i })
      .filter({ hasText: "Superseded" }),
  ).toHaveCount(1);

  await signIn(page, owner);
  await page.goto("/");
  await fakeAnswerModel(page);
  await page.getByRole("searchbox", { name: "Question" }).fill("Who owns the Payments API?");
  await page.getByRole("button", { name: "Ask", exact: true }).click();
  await expect(page.getByRole("button", { name: "Open claim C1" })).toBeVisible({ timeout: 15_000 });
  // The answer context holds the new owner of the service, and not the old one.
  const stream: string = await page.evaluate(async () => {
    const r = await fetch("/api/v1/ask", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ question: "Who owns the Payments API?" }),
    });
    return r.text();
  });
  const context = stream
    .split("\n")
    .filter((l) => l.startsWith("data: "))
    .flatMap(
      (l) =>
        (JSON.parse(l.slice(6)).claims ?? []) as {
          subjectKey: string;
          predicate: string;
          objectKey: string;
        }[],
    );
  const owners = context
    .filter((c) => c.subjectKey === paymentsAPI && c.predicate === "owned_by")
    .map((c) => c.objectKey);
  expect(owners).toEqual([remindersTeam]);

  const methods = await fixtureMethods();
  expect(methods.length).toBeGreaterThan(0);
  for (const m of methods) expect(["GET", "HEAD"]).toContain(m);
});

test("SCN-REV-002 each card kind appears in the queue, and the reviewer does not see a card with restricted evidence", async ({
  page,
}) => {
  await signIn(page, owner);
  await page.goto("/");
  await github(page);
  await restricted(page);
  seedService("e2e:service:kinds", "Kinds Service");
  const kinds = {
    correction: "Correction",
    finding: "Finding",
    merge: "Merge",
    mention_link: "Mention link",
    unnamed: "Unnamed thing",
    new_term: "New term",
    term_edit: "Term edit",
    conflict: "Conflict",
    needs_edit: "Page needs edit",
  };
  const values = Object.keys(kinds)
    .map(
      (k) =>
        `('${k}', 'entity:e2e:service:kinds', '{"note": "seeded ${k}"}', 'system', ARRAY[(SELECT id FROM connections WHERE name = 'acme-github')])`,
    )
    .join(",\n");
  sql(`INSERT INTO proposals (kind, target, payload, via, connections) VALUES ${values};
INSERT INTO proposals (kind, target, payload, via, connections)
VALUES ('finding', 'entity:e2e:service:kinds', '{"note": "seeded restricted"}', 'system',
        ARRAY[(SELECT id FROM connections WHERE name = 'acme-restricted')]);`);

  await signIn(page, reviewer);
  await page.goto("/review");
  await page.getByLabel("Search cards").fill("seeded");
  for (const [kind, label] of Object.entries(kinds)) {
    await page.getByLabel("Kind").selectOption(kind);
    const card = page.getByTestId("card");
    await expect(card).toContainText(label);
    await expect(card).toContainText(`seeded ${kind}`);
  }
  await page.getByLabel("Kind").selectOption("finding");
  await page.getByLabel("Search cards").fill("seeded restricted");
  await expect(page.getByText("No card needs a decision.")).toBeVisible();

  await signIn(page, owner);
  await page.goto("/review");
  await page.getByLabel("Search cards").fill("seeded restricted");
  await expect(page.getByTestId("card")).toContainText("seeded restricted");
});

test("SCN-REV-005 after a sync, Updates shows a changed page, a new conflict and a new term under their entities", async ({
  page,
}) => {
  await signIn(page, owner);
  await page.goto("/");
  const id = await connectionId(page, "acme-public");
  expect(id).toBeTruthy();
  await appendToPage(
    "1008",
    `<table><tbody><tr><th>Term</th><th>Definition</th></tr>
<tr><td>Editor</td><td>The person who checks the language of a manuscript.</td></tr>
<tr><td>Peer review</td><td>The check of a manuscript by other experts.</td></tr></tbody></table>`,
  );
  await syncNow(page, id as number);

  await page.goto("/updates");
  const editor = page
    .getByTestId("update")
    .filter({ has: page.getByRole("link", { name: "Editor", exact: true }) });
  await expect(editor).toContainText("Changed");
  await expect(editor).toContainText("Acme glossary");
  await expect(editor).toContainText("New conflict");
  const peer = page
    .getByTestId("update")
    .filter({ has: page.getByRole("link", { name: "Peer review", exact: true }) });
  await expect(peer).toContainText("New term");
});

test("SCN-REV-007 cards show band, precision and reasons; keys, edits, bulk accept and tabs work", async ({
  page,
}) => {
  await signIn(page, owner);
  await page.goto("/");
  await github(page);
  const services: [string, string][] = [
    ["e2e:pair:zeta-a", "Zeta"],
    ["e2e:pair:zeta-b", "Zeta svc"],
    ["e2e:pair:wolf-a", "Wolf"],
    ["e2e:pair:wolf-b", "Wolf tool"],
    ["e2e:pair:xeno-a", "Xeno"],
    ["e2e:pair:xeno-b", "Xeno pay"],
    ["e2e:pair:yolo-a", "Yolo"],
    ["e2e:pair:yolo-b", "Yolo pay"],
    ["e2e:pair:auto-a", "Auto"],
    ["e2e:pair:auto-b", "Auto job"],
  ];
  for (const [k, n] of services) seedService(k, n);
  // Reviewed outcomes give the Medium band a measured precision.
  for (let i = 0; i < 3; i++) seedPair(`e2e:old:${i}a`, `e2e:old:${i}b`, 0.6, "medium", [], "accepted");
  seedPair("e2e:pair:zeta-a", "e2e:pair:zeta-b", 0.6, "medium", ["The names share the word Zeta."]);
  seedPair("e2e:pair:wolf-a", "e2e:pair:wolf-b", 0.1, "low", ["The names differ."]);
  seedPair("e2e:pair:xeno-a", "e2e:pair:xeno-b", 0.97, "high", ["Same owner and repository."]);
  seedPair("e2e:pair:yolo-a", "e2e:pair:yolo-b", 0.96, "high", ["Same owner."]);
  seedPair("e2e:pair:auto-a", "e2e:pair:auto-b", 0.99, "high", ["Same name."], "auto");

  await signIn(page, reviewer);
  await page.goto("/review");
  await page.getByLabel("Kind").selectOption("merge");
  await page.getByLabel("Search cards").fill("e2e:pair");
  const card = page.getByTestId("card");

  // The Medium card comes first: it is the most uncertain.
  await expect(card).toContainText("Zeta");
  await expect(card).toContainText("Medium");
  await expect(card.getByTestId("precision")).toContainText("Precision 1.00 in this band");
  await expect(card.getByTestId("reasons")).toContainText("The names share the word Zeta.");

  // E edits the canonical name; accept uses it.
  await focusQueue(page);
  await page.keyboard.press("e");
  await card.getByLabel("Canonical name").fill("Zeta Service");
  const zeta = decided(page, "accept");
  await card.getByRole("button", { name: /^Accept/ }).click();
  await expect(card).toContainText("Wolf");
  expect((await zeta).status()).toBe(204);
  expect(await nameOf(page, "e2e:pair:zeta-a")).toBe("Zeta Service");

  // A then U brings the card back; S skips; R asks for a reason.
  const wolf = await card.getAttribute("data-card");
  await focusQueue(page);
  await page.keyboard.press("a");
  await expect(page.getByText("Press U to undo.")).toBeVisible();
  await expect(card).not.toHaveAttribute("data-card", wolf ?? "");
  await page.keyboard.press("u");
  await expect(card).toHaveAttribute("data-card", wolf ?? "");
  await page.keyboard.press("s");
  await expect(card).not.toHaveAttribute("data-card", wolf ?? "");
  await page.getByRole("button", { name: /Wolf/ }).click();
  await expect(card).toHaveAttribute("data-card", wolf ?? "");
  await focusQueue(page);
  await page.keyboard.press("r");
  await page.getByRole("dialog").getByLabel("Reason").fill("Different tools.");
  const rejected = decided(page, "reject");
  await page.getByRole("dialog").getByRole("button", { name: "Send" }).click();
  expect((await rejected).status()).toBe(204);

  // Bulk accept of High in a filter decides only those cards.
  await page.getByLabel("Band").selectOption("high");
  await page.getByLabel("Search cards").fill("Xeno");
  await page.getByRole("button", { name: "Accept all High" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Accept all High" }).click();
  await expect(page.getByText("1 cards accepted.")).toBeVisible();
  const open = (await openCards(page, "merge")).flatMap((c) => c.subjects.map((s) => s.key));
  expect(open).toContain("e2e:pair:yolo-a");
  expect(open).not.toContain("e2e:pair:xeno-a");

  // The tabs list the right cards.
  await page.getByRole("tab", { name: "Decided" }).click();
  await page.getByLabel("Band").selectOption("");
  await page.getByLabel("Search cards").fill("e2e:pair");
  const rows = page.getByTestId("decided");
  await expect(rows.filter({ hasText: "Wolf" })).toContainText("Rejected");
  await expect(rows.filter({ hasText: "Wolf" })).toContainText("Different tools.");
  await expect(rows.filter({ hasText: "Xeno" })).toContainText("Accepted");
  await expect(rows.filter({ hasText: "Yolo" })).toHaveCount(0);
  await page.getByRole("tab", { name: "Auto-applied" }).click();
  await expect(page.getByTestId("auto").filter({ hasText: "Auto" })).toHaveCount(1);
  await page.getByRole("tab", { name: "Needs decision" }).click();
  await page.getByLabel("Kind").selectOption("merge");
  await page.getByLabel("Search cards").fill("Yolo");
  await expect(card).toContainText("Yolo");
});

test("SCN-REV-010 Merge with proposes for a member and merges for a reviewer; Not the same and Split work from the page", async ({
  page,
}) => {
  await signIn(page, owner);
  await page.goto("/");
  await github(page);
  seedService("e2e:service:mike-a", "Mike");
  seedService("e2e:service:mike-b", "Mike bis");
  seedService("e2e:service:mike-c", "Mike ter");

  const mergeWith = async (key: string, other: string, button: string) => {
    await page.goto(pageURL(key));
    await page.getByRole("button", { name: "Merge with…" }).click();
    const dialog = page.getByRole("dialog");
    await dialog.getByLabel("Search entities").fill(other.split(" ")[0] ?? other);
    await dialog.getByRole("button", { name: `Select ${other}`, exact: true }).click();
    await dialog.getByRole("button", { name: button }).click();
  };

  await signIn(page, member);
  await mergeWith("e2e:service:mike-a", "Mike bis", "Propose merge");
  await expect(page.getByText("A reviewer gets your merge proposal.")).toBeVisible();
  await expect(page.getByRole("button", { name: "Not the same" })).toHaveCount(0);

  await signIn(page, reviewer);
  const proposals = (await openCards(page, "merge")).filter((c) =>
    c.subjects.some((s) => s.key === "e2e:service:mike-b"),
  );
  expect(proposals).toHaveLength(1);

  await mergeWith("e2e:service:mike-a", "Mike ter", "Merge");
  await expect(page.getByText("The entities are merged.")).toBeVisible();
  await expect(page.getByText("Merged entities")).toBeVisible();
  await expect(page.getByText("Mike ter", { exact: true })).toBeVisible();

  // Not the same closes the merge card of the member.
  await page.getByRole("button", { name: "Not the same" }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("Search entities").fill("Mike");
  await dialog.getByRole("button", { name: "Select Mike bis", exact: true }).click();
  await dialog.getByRole("button", { name: "Mark as different" }).click();
  await expect(page.getByText("Assay keeps the two entities apart.")).toBeVisible();
  expect(
    (await openCards(page, "merge")).filter((c) => c.subjects.some((s) => s.key === "e2e:service:mike-b")),
  ).toHaveLength(0);

  // Split undoes the merge.
  await page.getByRole("button", { name: "Split" }).click();
  await expect(page.getByText("Merged entities")).toHaveCount(0);
  const c = await call(page, "GET", `/api/v1/entities/${encodeURIComponent("e2e:service:mike-c")}`);
  expect(c.json.key).toBe("e2e:service:mike-c");
});

test("SCN-UI-001 the member sees no Review or admin Settings; the admin palette finds an entity, a term and the Models page", async ({
  page,
}) => {
  await signIn(page, member);
  await page.goto("/");
  const nav = page.getByRole("navigation", { name: "Main" });
  await expect(nav.getByRole("link", { name: "Updates" })).toBeVisible();
  await expect(nav.getByRole("link", { name: "Review" })).toHaveCount(0);
  await expect(nav.getByRole("link", { name: "Settings" })).toHaveCount(0);
  await page.goto("/review");
  await expect(page).toHaveURL(/\/$/);
  await page.goto("/settings/models");
  await expect(page).not.toHaveURL(/settings/);

  await signIn(page, reviewer);
  await page.goto("/");
  await expect(
    page.getByRole("navigation", { name: "Main" }).getByRole("link", { name: "Review" }),
  ).toBeVisible();

  await signIn(page, owner);
  await page.goto("/");
  const palette = async (text: string) => {
    await page.keyboard.press("ControlOrMeta+k");
    const box = page.getByRole("dialog");
    await box.getByRole("combobox").fill(text);
    return box;
  };
  let box = await palette("Payments API");
  await expect(box.getByRole("option", { name: /Payments API/ }).first()).toBeVisible();
  await page.keyboard.press("Escape");
  box = await palette("Manuscript");
  await expect(box.getByRole("option", { name: /Manuscript/ }).first()).toBeVisible();
  await page.keyboard.press("Escape");
  box = await palette("Models");
  await box
    .getByRole("option", { name: /Models/ })
    .first()
    .click();
  await expect(page).toHaveURL(/\/settings\/models$/);
});
