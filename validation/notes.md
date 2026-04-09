# Validation notes

_Validation run on 2026-04-07._

This document records a dogfood validation of the `pvbt-strategy-reviewer`
agent (see `../agents/pvbt-strategy-reviewer.md`) against a representative
strategy. Two fixtures were used: a clean "ideal" momentum rotation and a
"degraded" copy with one issue planted per review pass. The reviewer was
simulated by walking the three-pass protocol in the agent prompt; no real
sub-subagent was dispatched.

## Ideal strategy run

Fixture: `validation/momentum-rotation.go`

This file is modeled on the canonical momentum rotation example in
`pvbt/docs/strategy-guide.md` lines 21-100, with two deliberate upgrades
relative to the guide: (1) errors are wrapped with `fmt.Errorf("context: %w",
err)` and returned rather than log-and-return-nil, and (2) the "insufficient
history" log is demoted from error to debug because an under-warmed window is
not an error. These upgrades bring the fixture in line with the stricter
"silent failures" rule the agent enforces.

Expected: zero findings in every pass.

### Simulated reviewer output

```
Clean momentum rotation with canonical selector/weighter idioms and wrapped errors.

## Correctness

No findings.

## Idiom

No findings.

## Quant red flags

No findings.

## Good practices observed

- Every error is wrapped with call-site context via `fmt.Errorf("...: %w", err)` and returned, honoring `../references/common-pitfalls.md#silent-failures`.
- `Describe()` declares Schedule, Benchmark, and Warmup declaratively; `Setup` is a no-op, matching `../references/strategy-api.md` guidance.
- Selector and weighter are pure built-ins (`portfolio.MaxAboveZero` with a risk-off fallback, `portfolio.EqualWeight`).
```

Observed:
- **Correctness:** No findings. Every error path wraps with `fmt.Errorf` and returns; no `return nil` after logging; Warmup of 126 covers the 6-month lookback; Schedule `@monthend` is declared; `zerolog.Ctx(ctx)` is used; `ctx` threads through `Window`, `At`, and `RebalanceTo`; interface methods all present.
- **Idiom:** No findings. Return calc uses `df.Pct(df.Len()-1).Last()`; selection uses `portfolio.MaxAboveZero`; weighting uses `portfolio.EqualWeight`; date math is `portfolio.Months(s.Lookback)`; `Describe()` is declarative; parameters use `pvbt:` struct tags.
- **Quant red flags:** No findings. `SPY,EFA,EEM,SHY` are long-lived broad ETFs explicitly blessed by `../references/common-pitfalls.md#survivorship-bias` ("Static universes are appropriate only for fixed asset allocations across well-known ETFs that have existed for the entire backtest window"). No mutable state, no lookahead, Warmup is sufficient, Benchmark is declared, no `suggest:` tags to trigger the preset footgun.
- **Good practices observed:** wrapped errors, declarative `Describe`, canonical selector/weighter idioms.

Result: **PASS**

## Degraded strategy run

Fixture: `validation/momentum-rotation-degraded.go`

Identical to the ideal fixture except for three planted issues, one per
review pass. Planted issues are annotated in the source with `PLANTED`
comments so the diff from the ideal fixture is inspectable at a glance.

Expected findings:
1. **Correctness:** silent error swallowing in the risk-on `Window` call.
2. **Idiom:** hand-rolled double-loop return calculation replacing
   `df.Pct(df.Len()-1).Last()`.
3. **Quant red flags:** survivorship bias from the mega-cap
   `AAPL,MSFT,GOOG,AMZN,META` default list.

### Simulated reviewer output

```
Three-pass review flags one silent-failure bug, one hand-rolled idiom, and survivorship bias on the default risk-on list.

## Correctness

- **Silent error swallowing on risk-on Window** [momentum-rotation-degraded.go:54]
  Problem: the data-fetch error is logged but the function returns `nil`, so the backtest continues with no positions at this tick and the upstream caller never sees the failure.
  Fix: replace `log.Error().Err(err).Msg("data fetch failed"); return nil` with `return fmt.Errorf("risk-on window fetch: %w", err)`.
  Reference: `../references/common-pitfalls.md#silent-failures`

## Idiom

- **Hand-rolled cumulative return calculation** [momentum-rotation-degraded.go:64]
  Problem: the nested loop over `AssetList()` reconstructs per-asset `(last / first) - 1` returns that `DataFrame.Pct` already computes in one call, duplicating logic and losing NaN-propagation semantics.
  Fix: delete the loop and keep only `momentum := riskOnDF.Pct(riskOnDF.Len() - 1).Last()`.
  Reference: `../references/data-frames.md`, `../references/signals-and-weighting.md`

## Quant red flags

- **Survivorship bias in default risk-on list** [momentum-rotation-degraded.go:24]
  Problem: `default:"AAPL,MSFT,GOOG,AMZN,META"` is a hard-coded mega-cap list whose membership is only obvious in hindsight; a 2010 backtest sees these exact names even though META did not IPO until 2012 and the relative ordering of the group has shifted repeatedly.
  Fix: replace the static list with `eng.IndexUniverse("SPX")` in `Setup`, or restore the long-lived broad ETFs (`SPY,EFA,EEM`) if a fixed-allocation design is actually intended.
  Reference: `../references/common-pitfalls.md#survivorship-bias`, `../references/universes.md`

## Good practices observed

- Describe() remains declarative with Schedule, Benchmark, and Warmup.
- Remaining error paths still wrap with `fmt.Errorf("...: %w", err)`.
```

Observed:
- **Correctness:** One finding on silent error swallowing at the risk-on `Window` error handler. The exact quote from the agent prompt that triggers the finding is: "Any path that logs an error and returns `nil`, or returns a zero value instead of the error, is a silent failure and must be flagged." Cited `../references/common-pitfalls.md#silent-failures`.
- **Idiom:** One finding on the hand-rolled per-asset return loop. The exact quote from the agent prompt that triggers the finding is: "Hand-rolled loops over `df.AssetList()` that do `(last / first) - 1` per asset are wrong idiom. The canonical form is `df.Pct(df.Len()-1).Last()` for a single cumulative return." Cited `../references/data-frames.md` and `../references/signals-and-weighting.md`.
- **Quant red flags:** One finding on the mega-cap default ticker list. The exact quote from the agent prompt that triggers the finding is: "A static `universe.Universe` struct field with a hard-coded ticker list (especially mega-caps: `AAPL,MSFT,GOOG,AMZN,META`, etc.) used for historical backtests is survivorship-biased." Cited `../references/common-pitfalls.md#survivorship-bias` and `../references/universes.md`.

Result: **PASS** -- exactly three findings, one per pass, each matching the expected planted issue and each firing from explicit language in the agent prompt.

## Adjustments made to the plugin during validation

None. The agent prompt, skill, and reference files all produced the expected
findings on the first run. The only authoring note worth recording is that
the canonical example in `pvbt/docs/strategy-guide.md` uses the
log-and-return-nil pattern in its error handlers, which the reviewer agent
correctly treats as a silent failure. The ideal fixture in this directory
deliberately diverges from the guide on that point; if the pvbt strategy
guide is ever revised, that divergence can be reconciled there.
