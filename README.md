# pvbt-strategy-author

A Claude Code plugin for authoring and reviewing [pvbt](https://github.com/penny-vault/pvbt) quantitative trading strategies.

## What it does

- **Design** a new strategy by describing it in natural language. The `pvbt-strategy-design` skill extracts intent, fills sensible defaults, and only asks about genuine gaps or risks. It supplements the `superpowers:brainstorming` skill with pvbt-specific knowledge rather than running its own flow.
- **Review** an existing strategy with the `pvbt-strategy-reviewer` agent. It checks correctness, pvbt idiom, and quant red flags such as survivorship bias and lookahead bias, citing the specific reference section that explains each finding.

## Requirements

- Claude Code
- pvbt 0.6.0 or newer

## Install

Install from the Claude Code marketplace:

```
/plugin install pvbt-strategy-author
```

Or install from source:

```bash
git clone https://github.com/penny-vault/pvbt-strategy-author
/plugin install ./pvbt-strategy-author
```

## Usage

### Designing a strategy

Describe your strategy idea in natural language. The design skill activates automatically alongside the brainstorming skill:

> "I want a monthly momentum rotation across SPY, EFA, EEM with a 6-month lookback, falling back to SHY when nothing is positive."

The skill extracts your intent, fills defaults, and only asks clarifying questions when something is genuinely ambiguous or risky. The resulting design document contains a filled-out `## pvbt strategy spec` section you can hand straight to code generation.

### Reviewing a strategy

After writing strategy code, the reviewer agent runs automatically when Go files that import `github.com/penny-vault/pvbt` are changed. You can also invoke it explicitly:

> "Review my strategy."

The reviewer returns a structured report with three sections:

- **Correctness** — interface implementation, error handling, read-only portfolio, batch-based orders, warmup sizing, schedule validity.
- **Idiom** — use of built-in selectors and weighting functions, declarative `Describe()`, universe `Window`/`At`, presets via struct tags.
- **Quant red flags** — survivorship bias, lookahead bias, leaked state, insufficient warmup, over-parameterization, the `--preset` naming footgun.

Each finding cites the exact reference section that explains the principle.

## How it works

- `agents/pvbt-strategy-reviewer.md` — the reviewer agent definition.
- `skills/pvbt-strategy-design/SKILL.md` — the design skill definition.
- `references/` — nine curated pvbt reference documents consumed by the agent and skill on demand.

The plugin carries its pvbt knowledge with it. It does not require the pvbt source tree to be available at runtime. Each reference file declares the pvbt version it was last verified against.

## Versioning

This plugin uses semantic versioning. Major releases track breaking pvbt API changes. Minor releases add capabilities. Patch releases are plugin-only fixes. The current release targets pvbt 0.6.0.

## License

Apache 2.0. See [LICENSE](./LICENSE).
