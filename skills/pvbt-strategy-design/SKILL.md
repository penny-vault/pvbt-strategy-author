---
name: pvbt-strategy-design
description: Use when designing, brainstorming, or drafting a pvbt quantitative trading strategy. Extracts strategy intent from the author's description, fills in sensible defaults, and only pauses the brainstorm for genuine ambiguity or risk.
---

# pvbt-strategy-design

This skill supplements the `superpowers:brainstorming` skill with pvbt-specific knowledge. It does **not** run its own brainstorming flow; brainstorming still owns the conversation. While brainstorming, use this skill to translate the author's natural-language description into the ten-slot pvbt strategy schema, fill in sensible defaults, and pause only when something is genuinely ambiguous or risky. Do not interrogate the author. If a slot has a safe default, take the default and note it in the design doc.

## When this skill fires

Activate alongside `superpowers:brainstorming` whenever the user describes a quantitative trading strategy that will be implemented in pvbt. Typical triggers: "I want to build a strategy that...", "let's brainstorm a momentum rotation", "design a strategy that buys...", or any natural-language pitch for a backtest or live strategy.

This skill never runs alone. It is a translation layer the brainstorming skill leans on.

## Strategy schema

A pvbt strategy is fully described by ten slots. Every design conversation must end with all ten filled in (with defaults marked).

1. **Universe** -- the asset list the strategy considers. Static, index-tracking, or US-tradable.
2. **Schedule** -- the tradecron expression that decides when `Compute` runs.
3. **Signal** -- the numeric measure used to rank or filter assets.
4. **Selection** -- how the signal turns into a chosen subset (TopN, BottomN, MaxAboveZero, etc.).
5. **Weighting** -- how the chosen subset turns into target weights.
6. **Warmup** -- the trading-day history the engine must pre-fetch before the first `Compute`.
7. **Benchmark + Risk-free asset** -- benchmark for reporting; risk-free is `DGS3MO`, hard-coded in pvbt.
8. **Parameters** -- the knobs the strategy exposes via `StrategyDescription.Parameters`.
9. **Presets** -- named parameter bundles for the `--preset` flag.
10. **Risk management** -- stop losses, position limits, drawdown controls, or "none".

## Extraction rules

Translate natural-language phrases to slot values without asking for confirmation. The mappings below are minimums; apply analogous reasoning to similar phrasings.

**Schedule:**
- "daily" -> `@daily`
- "weekly" / "every week" -> `@weekbegin` or `@weekend` (this one *is* ambiguous; ask only when neither is implied by context like "rebalance Friday close")
- "monthly" / "every month" -> `@monthend`
- "quarterly" / "every quarter" -> `@quarterend`
- "yearly" / "annually" -> `@yearend`
- "rebalance" mentioned without cadence -> `@monthend` (default)

**Warmup:**
- "N-month lookback" -> warmup approximately `N * 21` trading days
- "N-day lookback" -> warmup approximately `N` trading days
- "N-year lookback" -> warmup approximately `N * 252` trading days
- Multiple lookbacks declared -> warmup = the largest of them
- Parameter-driven lookback -> warmup = the maximum the parameter could plausibly take, not its default

**Selection:**
- "rotate into the best" / "rotate" -> `MaxAboveZero` selection + `EqualWeight`
- "top K" / "best K" -> `TopN(K)` selection + `EqualWeight`
- "bottom K" / "cheapest K" / "worst K" -> `BottomN(K)` selection + `EqualWeight`
- "pick the best" with no count -> `TopN(1)`
- "all positive" / "everything above zero" -> `MaxAboveZero`

**Signal:**
- "momentum" unqualified -> total return over the stated lookback
- "risk-adjusted momentum" / "Sharpe-like momentum" -> `RiskAdjustedPct`
- "price vs moving average" / "trend" -> price relative to MA over the lookback
- "value" / "cheapest by P/E" -> trailing P/E ratio (warn about lookahead; see red flags)
- "low volatility" -> trailing standard deviation of returns

**Universe:**
- "US stocks" / "liquid US stocks" / "US equities" -> `universe.USTradable()`
- "S&P 500" / "SPX" -> `eng.IndexUniverse("SPX")`
- "Nasdaq 100" / "NDX" -> `eng.IndexUniverse("NDX")`
- "Russell 2000" / "RUT" -> `eng.IndexUniverse("RUT")`
- A short list of named tickers -> `universe.NewStatic(...)` (warn about survivorship bias if the backtest is historical; see red flags)

**Risk-off / fallback:**
- "fall back to X when nothing is positive" -> `MaxAboveZero` with `RiskOff` parameter set to X; X is added to the universe
- "go to cash" -> risk-off `SHY` (treasury bills, short duration)

## Default table

Apply these defaults silently. Mark each in the output spec with `(default)`.

| Slot | Default |
|------|---------|
| Benchmark | First asset in the primary universe if narrow; `SPY` if broad US equity |
| Risk-free asset | `DGS3MO` (hard-coded in pvbt; not configurable via `StrategyDescription`) |
| Weighting | `EqualWeight` |
| Warmup | Derived from the longest declared lookback |
| Schedule | `@monthend` if "rebalance" is mentioned without cadence |
| Selection | `MaxAboveZero` if "rotate"; `TopN(1)` if "pick the best" |
| Parameters | Only the stated knobs of the idea. Start from two; add a third only when the idea genuinely requires it, a fourth only when the author explicitly asks |
| Presets | None unless named variants are mentioned |
| Risk management | None unless explicitly requested |

## Ambiguity triggers

Pause the brainstorm only for these situations. Maximum of six.

1. **Signal math is vague** -- the description says "momentum", "value", "trend", or similar but it could be raw return, risk-adjusted return, or price-vs-MA, and context does not pick one. This trigger also fires when the signal type *is* clear (e.g. "trailing P/E") but the lookback window is unspecified and no default makes sense.
2. **Selection count undeclared** -- the description says "top" or "best" without a count or percentage.
3. **Rebalance cadence neither stated nor implied** -- no cadence verb appears anywhere ("monthly", "quarterly", "rebalance", etc.).
4. **Exit rules mentioned but not detailed** -- the author hints at stop-losses, trailing stops, or exit conditions without specifying the trigger.
5. **Universe ambiguous between static and index-tracking** -- "the S&P 500" could mean today's constituents (static) or `IndexUniverse("SPX")` (point-in-time membership). Default to `IndexUniverse` unless the author says "the current S&P 500".
6. **Multiple filters or thresholds implied** -- the description stacks conditions ("trend filter AND volatility cap AND regime gate", or a signal plus a z-score plus an entry threshold plus a holding cap). Ask which one is essential to the idea rather than exposing them all as parameters. Degrees of freedom are the scarce resource; the answer guides the minimum viable parameter set in the spec.

**Do not ask about slots where a default is safe. Note the default in the design doc instead.** The author wants a strategy spec, not a questionnaire.

## Red flag triggers

Warn proactively when any of these appear in the description, even if the author did not ask. Each warning must cite the relevant reference file so the author can read the full explanation.

1. **Survivorship bias** -- the description names specific tickers chosen with knowledge of their later performance (e.g., "AAPL, MSFT, GOOG, AMZN from 2000 to today"). The ticker list itself encodes the answer. Cite [`../../references/common-pitfalls.md`](../../references/common-pitfalls.md) (Survivorship bias section).
2. **Lookahead bias** -- the signal uses fundamentals, index membership, or any data that may not have been available at simulation time `t`. Trailing P/E, trailing earnings, "current S&P 500 constituents", and index reconstitutions all qualify. Cite [`../../references/common-pitfalls.md`](../../references/common-pitfalls.md) (Lookahead bias section).
3. **Over-parameterization** -- the parameter count exceeds 3-4 without clear justification, or every part of the strategy is exposed as a knob. Cite [`../../references/common-pitfalls.md`](../../references/common-pitfalls.md) (Over-parameterization section).
4. **Insufficient warmup** -- the warmup implied by the longest lookback is comparable to or larger than the proposed backtest window, leaving too little out-of-sample data. Cite [`../../references/common-pitfalls.md`](../../references/common-pitfalls.md) (Insufficient warmup section).
5. **Selection bias from a static list** -- a static universe excludes obvious failure cases (no delisted stocks, no bankruptcies, no unprofitable years). Cite [`../../references/common-pitfalls.md`](../../references/common-pitfalls.md) (Survivorship bias section).

A red flag is a warning, not a stop. Surface the risk, recommend the fix, then continue the brainstorm.

## Output contract

When the brainstorming skill writes the design doc, ensure it contains a `## pvbt strategy spec` section with one line per schema slot. Slots filled by default must be marked `(default)`. Slots filled because the author was asked must annotate the question and answer. Flagged risks go in a `### Flagged risks` subsection at the end.

Example:

```markdown
## pvbt strategy spec

- **Universe:** `universe.USTradable()` (default)
- **Schedule:** `@quarterend`
- **Signal:** trailing 12-month P/E ratio (asked: lookback window was not specified)
- **Selection:** `BottomN(N)`, where `N` = 10% of the universe size
- **Weighting:** `EqualWeight` (default)
- **Warmup:** 252 trading days
- **Benchmark:** SPY (default)
- **Risk-free asset:** DGS3MO (hard-coded)
- **Parameters:** `PercentileCut` (default 0.10), `RebalanceSchedule`
- **Presets:** none
- **Risk management:** none (note: consider a stop-loss for single-name concentration)

### Flagged risks
- Lookahead bias: trailing P/E uses reported earnings; verify data provider supplies only post-announcement values.
```

## See also

- [`../../references/strategy-api.md`](../../references/strategy-api.md) -- `Strategy`, `Descriptor`, `StrategyDescription`, lifecycle.
- [`../../references/scheduling.md`](../../references/scheduling.md) -- tradecron expressions, warmup semantics, time zones.
- [`../../references/universes.md`](../../references/universes.md) -- static, index, rated, and US-tradable universes.
- [`../../references/signals-and-weighting.md`](../../references/signals-and-weighting.md) -- selection rules, weighting functions, signal helpers.
- [`../../references/parameters-and-presets.md`](../../references/parameters-and-presets.md) -- declaring parameters, defining presets, the `--preset` flag.
- [`../../references/portfolio-and-batch.md`](../../references/portfolio-and-batch.md) -- portfolio queries, batch order construction.
- [`../../references/data-frames.md`](../../references/data-frames.md) -- `DataFrame` API for signal computation.
- [`../../references/common-pitfalls.md`](../../references/common-pitfalls.md) -- survivorship, lookahead, leaked state, warmup, over-parameterization.
- [`../../references/testing-strategies.md`](../../references/testing-strategies.md) -- Ginkgo suite layout for strategy tests.
