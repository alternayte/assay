# Web documentation source

## What it does
An admin creates a `web` connection with a base URL, such as an internal developer portal or a vendor docs site. Assay reads the pages of that site from a seed: `llms.txt`, `llms-full.txt`, `sitemap.xml` or a start page. It follows links on the same host only, inside optional path prefixes, up to a depth and a page limit. Each page is an item with passages, so search, Ask, claims and get_item use it.

## Decisions
- The connection kind is `web`, in `internal/connectors/web`, added to `sync.Kinds` — it implements `connectors.Connector` with `Changed`, `ListIDs` and `Fetch`.
- Scope: `baseURL`, `seed` (`auto`, `llms`, `llms-full`, `sitemap`, `start`), `pathPrefixes`, `maxDepth` (default 3), `maxPages` (default 2,000) — the limits bound a site with generated pages.
- `auto` tries `/llms.txt`, then `/sitemap.xml`, then the base URL as a start page — `llms.txt` lists the pages that the site wants agents to read.
- `llms.txt` follows llmstxt.org: the H1 is the site title, and each `[name](url): notes` link under an H2 is a seed page. Links under `## Optional` are seeds too — the admin limits the scope with path prefixes, not by section.
- `llms-full.txt` becomes one item per top-level (`#`) section. The item URL is the URL of the first link in the section when it is on the same host, else `<base>/llms-full.txt#<slug>` — a section is one page of the site.
- For each page, the connector first sends HEAD to `<url>.md` (and `<dir>/index.html.md` for a URL that ends in `/`). On 200 it reads the Markdown — the site author wrote it for agents, and it needs no conversion.
- Otherwise the connector reads the HTML and converts it with `github.com/JohannesKaufmann/html-to-markdown/v2` (MIT). It takes `<main>`, else `<article>`, else `<body>`, and drops `nav`, `header`, `footer`, `aside`, `script`, `style` and forms — the Confluence storage converter reads storage format, not site HTML.
- Links in the Markdown keep only `http`, `https` and `mailto`, as in the Confluence converter.
- Traversal follows `<a href>` links from HTML and Markdown pages to pages on the same scheme and host, inside the path prefixes, breadth first. It drops the fragment and the query string — query variants are mostly the same page.
- The connector reads `/robots.txt` once per run and skips disallowed paths for the user agent `Assay`. It honours `Crawl-delay` as a lower rate limit than the connection setting.
- The connector client sends only GET and HEAD. It follows a redirect only to the same scheme and host, as `internal/outbound` does. A redirect to another host skips the page with the reason "The page redirects to another host."
- The item ID is the normalized URL. The revision is the `ETag`, else `Last-Modified`, else empty. `Changed` sends HEAD for each known page, and skips the GET when the revision is unchanged. Without a revision, `content.Store` compares the content hash.
- The page title is the `<title>` or the first H1, and `updatedAt` is `Last-Modified` when present.
- Reconcile lists the pages from the seed again. It tombstones a page that returns 404 or 410, or that is no longer reachable from the seed.
- An optional auth header (name and value) is stored as the connection secret — internal portals often sit behind a static token.
- Visibility follows the default rule: the owner first, then shares to groups.
- Each page is at most `MaxItemBytes`. Non-HTML and non-Markdown content types skip with the reason "The page is not HTML or Markdown."
- The connection form, the copy in `web/src/copy/en.json` and `docs/connectors.md` add the kind and its settings.

## Out
- JavaScript rendering.
- Crawling across hosts or subdomains.
- Forms, sign-in flows and cookies.
- PDF and other files linked from pages. The attachments spec covers file text.

## How I know it works
- Sync the Acme fixture site with `llms.txt`, nested links and one link to another host. Only same-host pages are items, and the other host gets no request.
- A fixture page with a `.md` variant is stored from the Markdown, and the fixture log shows no GET of its HTML.
- A second sync with unchanged ETags sends only HEAD requests.
- Remove a page from the fixture. The next reconcile tombstones it.
- A path that `robots.txt` disallows is not read.
- Ask with a question that a fixture page answers cites that page.
- The leak suite covers a `web` connection shared with one group only.
- The request log of the fixture server shows only GET and HEAD.
