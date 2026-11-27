# Console tab marker

## Intent

The tab bar mixes consoles (editable queries) with read-only views (table data,
structure, info, Kafka/ES/Redis browsers, diff, import…). They share one place,
so at a glance it is unclear which tab is a console. Mark consoles in the tab bar
so the two kinds are told apart immediately.

## Goals / non-goals

- Goal: a console tab is recognisable in the tab bar without reading its title.
- Non-goal: changing the layout (a "consoles on top, all results below" split was
  discussed and rejected: it matches DataGrip less, halves the height of table data,
  leaves the top empty for Kafka, and costs a rework of 20+ tab types and the keys).
- Non-goal: marking the result strip inside a console — it only holds one run's
  Summary and statements, and failed statements are already red.

## Approaches

- **Prefix glyph (chosen):** `✎` before the title of console tabs. Theme-independent,
  visible without colour.
- Colour only: needs a new theme slot; inactive tabs are all muted, so a custom
  theme may make the two kinds indistinguishable.
- Sort consoles first: renumbers tabs and moves the `[1]`…`[9]` jump targets.

## Design

- Console tabs: SQL console (`consoleTab`), ES console (`esConsoleTab`), Redis
  console (`redisConsoleTab`). Not the Kafka producer (a form, not a query editor).
- An optional interface on tabs reports "is a console"; `Model.tabBar()` prefixes
  the sanitised title with `✎` for those: `[1]✎orders.sql [+]  [2]users`.
- Unit test on the tab bar output; one `[Unreleased]` line in `CHANGELOG.md`.
  No keymap or README change.
