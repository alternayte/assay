# Vocabulary

A term is an entity of type term. Its synonyms and acronyms are its aliases. A term has a definition, a domain, an owner and a status: draft, approved or deprecated. A word with two meanings, such as "editor", gives two terms in different domains.

## The registry

The registry is a built-in connection, shared with Everyone. Members write records in it:

- a **thing**: type, name, aliases, description, owner, relations and links;
- a **term**: name, synonyms, definition, domain, owner, preferred term and related terms.

Each save is a version with the name of the editor. A record page shows the versions, a diff and a revert. A member's change to the definition of an approved term becomes a proposal for review. Only reviewers approve and deprecate terms.

## Where terms come from

- Glossary tables with Term and Definition columns on Confluence pages with a glossary label.
- "Full Name (ACRONYM)" patterns in pages and issues.
- Phrases that 3 or more items use across 2 or more connections that are shared with Everyone. When the decide model finds a phrase worthy, a New term card with a drafted definition goes to review.

## Glossary

The Glossary page searches and filters terms by status, domain and owner. A term page shows the definition, synonyms, deprecated forms, preferred term, owner, domain, related terms, sources, usage per connection and history. CSV export and import move terms in and out. `/glossary.md` gives the approved terms that you can read as Markdown.

## Terms while you read

Approved terms link in pages, answers and evidence passages: the whole word, the longest match, case-sensitive for an all-capital form, and the first use in each section. A popover shows the short definition, the status, the owner and a link. When a form matches two terms, the domain of the source selects one.

Answer and page prompts include the definitions of up to 20 approved terms in the context. The writing lint flags deprecated terms and names the preferred term. Search adds the synonyms and preferred forms of approved terms to a query.
