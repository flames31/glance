# Glance Config Editor — Design Notes

Handoff document from a Claude Code session exploring the [Glance](https://github.com/glanceapp/glance)
codebase. Written to be used as context for a follow-up session.

**Decision reached:** build a **standalone GUI config editor as a separate project**, rather than
forking Glance to add an in-app settings panel.

---

## 1. Background

Glance is a self-hosted dashboard. A single Go binary renders an HTML page from widgets declared in
YAML. There is **no settings UI at all** — the full route list (`internal/glance/glance.go:448`) is
pages, widget endpoints, auth, healthz, manifest, and static assets. Nothing writes config.

The only user-mutable state in the browser:
- **Theme picker** — `POST /api/set-theme/{key}` (`internal/glance/theme.go:15`). Only switches between
  presets already defined in YAML; stores the choice in a 2-year cookie.
- **Todo widget** — browser localStorage.
- **Collapse/expand, group tabs** — client-side view state.

Everything else (widgets, layout, pages, colors, auth) is YAML-only. The design bet is that
**auto-reload replaces the settings panel**: save the file, refresh, see the change. No restart, and
an invalid config keeps the previous one running.

---

## 2. Why NOT fork Glance to add a settings panel

### 2a. Upstream would never merge it

`README.md` contributing guidelines: "Avoid introducing new dependencies", "minimal vanilla JS",
"No `package.json`". A settings panel is a philosophical departure, not a feature gap. Any such fork
is permanent, with rebase conflicts forever.

### 2b. The config pipeline is strictly one-way

```
file → parseYAMLIncludes → parseConfigVariables → yaml.Unmarshal → initialize()
```

There is **no marshal path anywhere in the repo**. Three transforms run *before* parsing, all lossy:

1. **`$include` is a textual splice** — `internal/glance/config.go:295` returns a flat `[]byte` plus a
   `map[string]struct{}` of file paths. Zero provenance: nothing records which lines came from which
   file. Serializing a parsed config back collapses a multi-file setup into one giant file.

2. **`${ENV}` / `${secret:}` / `${readFileFromEnv:}` are regex-substituted** —
   `internal/glance/config.go:142`. By the time you hold a `config` struct, `token: ${secret:gh_token}`
   is already the literal token. Writing that struct back **plaintexts secrets into the config file.**
   This is the dangerous one.

3. **Comments are dropped** by `yaml.Unmarshal` into structs. Real-world configs are heavily commented.

Additionally, seven types have custom `UnmarshalYAML` with no `Marshal` counterpart (`hslColorField`,
`durationField`, `customIconField`, `proxyOptionsField`, `queryParametersField`, `releaseRequest`,
`widgets`), and `widgets` is **polymorphic** — dispatched on the `type` key at
`internal/glance/widget.go:27`. A naive round-trip means writing marshalers for ~26 widget types.

### 2c. Two non-obvious gotchas

- **Every config write wipes the cache.** The fsnotify watcher (`internal/glance/config.go:305`) can't
  distinguish an app-initiated write from a manual edit, and reload clears all cached widget data. A
  settings save would refetch every RSS feed and API on the page.
- **A settings panel is a privilege escalation.** Auth is flat — no roles, every user sees the same
  dashboard. A UI that can create `custom-api` / `html` / `extension` widgets is arbitrary HTML and
  template injection affecting all users.

---

## 3. The plan: a standalone editor

### Why external wins

- Zero rebase burden; Glance stays stock and upgradeable.
- Stack freedom — "no `package.json`" is their constraint, not ours.
- The privilege-escalation surface disappears; it's a local tool, not exposed to dashboard viewers.
- Usable by others without asking them to run a fork.

### Key insight: do not build a renderer

Glance already renders. Two facts verified in the codebase:

1. **No `X-Frame-Options`, no CSP, no `frame-ancestors` anywhere in the repo.** A running instance can
   be iframed.
2. `GET /api/pages/{page}/content/{$}` (`internal/glance/glance.go:451`, handler at
   `internal/glance/glance.go:334`) returns just the rendered page content as HTML.

**The loop:** editor writes YAML → Glance's fsnotify auto-reload picks it up → refresh the iframe.
Live preview, nearly free. The editor owns YAML manipulation; Glance owns the pixels.

### Do not build a validator either

Shell out to `glance --config <path> config:validate`. Perfect fidelity with the real parser,
including every custom `UnmarshalYAML` rule, for free. `config:print` resolves `$include`s for
debugging.

CLI subcommands available (`internal/glance/cli.go:50`): `config:validate`, `config:print`,
`secret:make`, `password:hash <pwd>`, `sensors:print`, `mountpoint:info`, `diagnose`.

---

## 4. Scope: layout, not forms

Field editing is **already solved** by [not-first/glance-schema](https://github.com/not-first/glance-schema)
— a JSON schema giving autocomplete, property docs, and validation in any IDE. Rebuilding that as web
forms is weeks of work to end up worse than what already exists.

The genuinely unsolved problem is **layout**: which widget, in which column, in what order, across
pages. That is what YAML expresses badly and a GUI expresses well.

### MVP

1. Load `glance.yml` (resolving `$include`s ourselves, tracking file boundaries).
2. Render pages → columns → widgets as a drag-and-drop tree.
3. Write back, preserving comments and formatting.
4. Iframe preview beside it, refreshed on save.

**No per-widget forms in v1.** Click a widget → raw YAML block in a small code editor (CodeMirror +
the JSON schema for autocomplete). This captures ~80% of the value and avoids the
"26 widget types × N properties each" form-building trap, which is where this class of project stalls.

---

## 5. Technical core: YAML round-tripping

Same hard problem as the in-repo panel, but solvable properly when we control the stack.

- **Use a comment-preserving AST.** In JS/TS, the `yaml` package (eemeli) `Document` API preserves
  comments and formatting through edits. This is the strongest argument for a TS front end over Go —
  `yaml.v3` Nodes do carry `HeadComment`/`LineComment`/`FootComment`, but it's fiddlier.
- **`$include` is *easier* outside Glance.** We resolve includes ourselves, so we know file boundaries
  and can write each change back to its source file. Glance structurally cannot do this — it discards
  provenance at `internal/glance/config.go:295`.
- **`${ENV}` stops being a problem.** Never resolve them. Display the literal `${GITHUB_TOKEN}` as an
  opaque chip in the UI. We never execute the config, so we never need the value — the secret-leak
  risk simply does not exist.

### Suggested stack

Vite + TS doing all YAML work, plus a ~100-line backend (Go or Node) for file read/write and shelling
out to `config:validate`. Alternatively skip the backend entirely with the File System Access API,
accepting Chrome-only.

### Known caveat

Every write triggers Glance's reload, which **wipes the widget cache** and refetches everything on the
page. Debounce saves, or point the preview at a scratch config with a couple of cheap widgets rather
than the real dashboard.

---

## 6. Appendix: easy first contributions to Glance itself

If also making changes in a fork of Glance, these are genuinely small. Widgets are self-contained:
one file, one template, one line in the `switch` at `internal/glance/widget.go:27`. No DI, no
registry, no codegen.

Ordered easiest → hardest:

1. **New icon library prefix** (~5 lines). Prefix switch at `internal/glance/config-fields.go:162`
   maps `si`/`di`/`mdi`/`sh` to jsDelivr URL patterns. Adding `lucide:` or `tabler:` is one `case`.
   Improves every widget that takes an `icon`.

2. **New style for an existing widget** (template-only). RSS already does this — four templates
   dispatched by an if-chain in `internal/glance/widget-rss.go:99`. Add a template to
   `internal/glance/templates/`, a `mustParseTemplate` var, and one branch. Teaches the templating
   layer with no fetching or caching involved.

3. **Configurable HTTP method + headers on the monitor widget** (~15 lines).
   `internal/glance/widget-monitor.go:147` hardcodes `http.MethodGet`. The widget already supports
   `alt-status-codes`, `allow-insecure`, and `timeout`, so the config-field pattern is right there to
   copy. Useful: many self-hosted services have `HEAD`-only or auth-gated health endpoints.

4. **A static/computed widget** (~40 lines Go + 1 template). Copy `internal/glance/widget-clock.go` —
   it has no `update()` at all; it renders once in `initialize()` into `cachedHTML`. A **countdown
   widget** ("42 days until X") is a real gap people currently fake with `custom-api`. Best first
   real feature: touches every layer, but the hard parts are absent.

5. **A fetch-based feed widget** (~130 lines, mostly copyable boilerplate). Model:
   `internal/glance/widget-lobsters.go`. Key leverage: `forumPostsTemplate`
   (`internal/glance/widget-shared.go:12`) is shared across Hacker News / Lobsters / Reddit, so a new
   link-aggregator widget needs only the fetch + JSON→`forumPostList` mapping. Teaches the real
   lifecycle: `initialize()` → `update(ctx)` → `canContinueUpdateAfterHandlingErr` → `Render()`.

### Checklist for any widget change

1. New file `internal/glance/widget-foo.go`
2. Register in the `switch` at `internal/glance/widget.go:27`
3. Template in `internal/glance/templates/` (embedded via `embed.go` — no build step)
4. Document in `docs/configuration.md`; every widget has a properties table, match the format
5. Test with `go run . --config <test config>` — auto-reload means edit YAML and refresh, no restart

Per contributing guidelines: no new dependencies, heroicons for new icons, stick to the existing
`primary` / `positive` / `negative` color variables. Items 1 and 5 are plausibly upstreamable; open a
feature request first, as they ask contributors to claim work before implementing.

---

## 7. Repo orientation quick reference

| Thing | Location |
|---|---|
| Entry point | `main.go` → `internal/glance.Main()` |
| CLI / flags / subcommands | `internal/glance/cli.go:50` (`--config`, default `glance.yml`) |
| Config structs | `internal/glance/config.go:30` |
| Config parse entry | `internal/glance/config.go:94` (`newConfigFromYAML`) |
| Variable substitution | `internal/glance/config.go:142` |
| `$include` resolution | `internal/glance/config.go:242`–`295` |
| File watcher / auto-reload | `internal/glance/config.go:305` |
| Route registration | `internal/glance/glance.go:448` |
| Widget registry (switch) | `internal/glance/widget.go:27` |
| Widget interface + base | `internal/glance/widget.go:124` |
| Custom YAML field types | `internal/glance/config-fields.go` |
| Templates (embedded) | `internal/glance/templates/` |
| Full config docs | `docs/configuration.md` (~3000 lines, property table per widget) |
| Starter config | `docs/glance.yml` |

Run locally: `go run . --config docs/glance.yml` → http://localhost:8080
(plain `go run .` fails with `open glance.yml: no such file or directory`; `/glance*.yml` is
gitignored, so no config is committed at the repo root.)
