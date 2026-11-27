# Console Tab Marker Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prefix console tabs with `✎` in the tab bar so consoles stand apart from read-only views.

**Architecture:** An optional `consoler` interface on tabs; `Model.tabBar()` (`internal/ui/app.go`) adds the glyph for tabs implementing it. No layout or keymap change.

**Tech Stack:** Go, Bubble Tea v2, Lip Gloss v2.

**Spec:** `docs/superpowers/specs/2026-11-27-console-tab-marker-design.md`

## Global Constraints

- Glyph is exactly `✎` (U+270E), placed between `[N]` and the title: `[1]✎console:app`.
- Marked: `consoleTab`, `esConsoleTab`, `redisConsoleTab`. Not marked: every other tab, the Kafka producer included.
- UI text in English; one line under `## [Unreleased]` in `CHANGELOG.md`.
- Commits: Conventional Commits with scope, no Claude co-author trailer.

## Review Focus

- Existing UI tests that match `[1]console:app` must be updated to the new label, not deleted.
- The glyph must not be put through `textutil.Sanitize` in a way that drops it — add it after sanitising the title.

---

### Task 1: Mark console tabs in the tab bar

**Files:**
- Modify: `internal/ui/tabs.go` (declare interface next to `framer`)
- Modify: `internal/ui/app.go` (`tabBar`, ~line 2062)
- Modify: `internal/ui/console.go`, `internal/ui/esconsole.go`, `internal/ui/redisconsole.go` (add method)
- Modify: `internal/ui/app_test.go` (`TestOpenConsoleTypeAndRun` expects `[1]✎console:app`)
- Test: `internal/ui/app_test.go`
- Modify: `CHANGELOG.md`

**Interfaces:**
- Produces: `type consoler interface{ console() }` in `tabs.go`; implemented by `*consoleTab`, `*esConsoleTab`, `*redisConsoleTab` with an empty `console()` method.

- [ ] **Step 1: Write the failing test** `TestTabBarMarksConsoles` in `app_test.go`: build a model with `newTestModel`, set `m.tabs` to a `newConsoleTab(...)`, a `newESConsoleTab(...)`, a `newRedisConsoleTab(...)` and a `newDataTab(...)` (fake backend, zero settings/timings), then on `ansi.Strip(m.tabBar())` assert it contains `"[1]✎"`, `"[2]✎"`, `"[3]✎"` and `"[4]"` not followed by `✎`. Also change `TestOpenConsoleTypeAndRun`'s expectation to `"[1]✎console:app"`.
- [ ] **Step 2: Run** `go test -race ./internal/ui -run 'TestTabBarMarksConsoles|TestOpenConsoleTypeAndRun'` — expect FAIL (no glyph).
- [ ] **Step 3: Implement** the interface, the three methods, and in `tabBar()` build the label as `fmt.Sprintf("[%d]%s%s", i+1, mark, textutil.Sanitize(t.Title()))` where `mark` is `"✎"` when `t` is a `consoler`.
- [ ] **Step 4: Run** `make test` and `make lint` — expect PASS.
- [ ] **Step 5: Add** to `CHANGELOG.md` under `## [Unreleased]`:
  ```
  ### Changed
  - Console tabs (SQL, Elasticsearch, Redis) are marked with `✎` in the tab bar, to tell them from table and info tabs.
  ```
- [ ] **Step 6: Commit** `feat(ui): mark console tabs with ✎ in the tab bar`.
