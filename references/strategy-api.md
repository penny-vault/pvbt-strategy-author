_Last verified against pvbt 0.6.0._

# Strategy API

## Interface

```go
package engine

type Strategy interface {
    Name() string
    Setup(eng *Engine)
    Compute(ctx context.Context, eng *Engine, portfolio portfolio.Portfolio, batch *portfolio.Batch) error
}
```

- `Name() string` -- short identifier used in logging, CLI output, and metadata.
- `Setup(eng *Engine)` -- one-time initialization called once after parameter hydration, before any `Compute` call.
- `Compute(ctx, eng, portfolio, batch) error` -- called at every scheduled tick. Reads data, decides allocations, writes orders to `batch`. Returning a non-nil error fails the run.

`Describe()` is provided by the optional `engine.Descriptor` interface (see below).

## Lifecycle

The engine drives a strategy in this order:

1. **Hydrate.** Reflection populates exported fields from `default` struct tags (and any `--flag` overrides from the CLI).
2. **Describe (optional).** If the strategy implements `engine.Descriptor`, `Describe()` is called once to read schedule, benchmark, warmup, and serializable metadata.
3. **Setup.** `Setup(eng)` is called once. Use it for imperative initialization that needs the live engine: registering an `IndexUniverse`, calling `eng.SetBenchmark`, building computed universes, etc.
4. **Step loop.** For every tick produced by the schedule, the engine fetches data for held assets, then calls `Compute(ctx, eng, port, batch)`. The strategy reads data, computes signals, and writes orders into `batch`. The engine then settles fills, updates the equity curve, and computes metrics.
5. **Return.** After the last tick the engine returns the final `portfolio.Portfolio` (in backtest mode) or keeps looping on the schedule (in live mode).

`Setup` runs **once**. `Compute` runs **once per scheduled tick**. `Describe` runs **once at startup**, before `Setup`.

## Minimal strategy

Complete, copy-paste runnable. `cli.Run` provides the entire CLI surface; no additional `main` plumbing is needed.

```go
package main

import (
    "context"

    "github.com/penny-vault/pvbt/cli"
    "github.com/penny-vault/pvbt/data"
    "github.com/penny-vault/pvbt/engine"
    "github.com/penny-vault/pvbt/portfolio"
    "github.com/penny-vault/pvbt/universe"
    "github.com/rs/zerolog"
)

type SimpleMomentum struct {
    Assets   universe.Universe `pvbt:"assets"   desc:"Assets to rotate between" default:"SPY,EFA,EEM" suggest:"Classic=SPY,EFA,EEM|Modern=VOO,VEA,VWO"`
    Lookback int               `pvbt:"lookback" desc:"Momentum lookback months" default:"6"`
}

func (s *SimpleMomentum) Name() string { return "simple-momentum" }

func (s *SimpleMomentum) Setup(_ *engine.Engine) {}

func (s *SimpleMomentum) Describe() engine.StrategyDescription {
    return engine.StrategyDescription{
        Schedule:  "@monthend",
        Benchmark: "SPY",
        Warmup:    126, // ~6 trading months
    }
}

func (s *SimpleMomentum) Compute(ctx context.Context, eng *engine.Engine, port portfolio.Portfolio, batch *portfolio.Batch) error {
    log := zerolog.Ctx(ctx)

    df, err := s.Assets.Window(ctx, portfolio.Months(s.Lookback), data.MetricClose)
    if err != nil {
        log.Error().Err(err).Msg("window fetch failed")
        return err
    }
    if df.Len() < 2 {
        return nil
    }

    momentum := df.Pct(df.Len() - 1).Last()
    portfolio.TopN(1, data.MetricClose).Select(momentum)

    plan, err := portfolio.EqualWeight(momentum)
    if err != nil {
        return err
    }
    return batch.RebalanceTo(ctx, plan...)
}

func main() {
    cli.Run(&SimpleMomentum{})
}
```

## StrategyDescription

`Describe()` returns the engine's `StrategyDescription` struct. All fields are optional, but `Schedule` is required somewhere -- either here or set imperatively in `Setup` -- or the engine will refuse to run.

```go
package engine

type Descriptor interface {
    Describe() StrategyDescription
}

type StrategyDescription struct {
    ShortCode   string    `json:"shortcode,omitempty"`
    Description string    `json:"description,omitempty"`
    Source      string    `json:"source,omitempty"`
    Version     string    `json:"version,omitempty"`
    VersionDate time.Time `json:"versionDate,omitzero"`
    Schedule    string    `json:"schedule,omitempty"`
    Benchmark   string    `json:"benchmark,omitempty"`
    Warmup      int       `json:"warmup,omitempty"`
}
```

| Field | Semantics |
|-------|-----------|
| `ShortCode` | Short identifier for serialization and reports. |
| `Description` | Human-readable summary; surfaced in `describe` output. |
| `Source` | Citation or URL for the strategy's origin. |
| `Version` | Semantic version of the strategy implementation. |
| `VersionDate` | Date the current version was authored. |
| `Schedule` | Tradecron expression that determines when `Compute` runs (e.g. `@monthend`, `@daily`, `0 10 * * *`). Eastern time, market-aware. |
| `Benchmark` | Ticker for the benchmark asset; powers Beta, Alpha, Tracking Error, Information Ratio. |
| `Warmup` | Trading days of historical data the strategy needs before its first tick. The engine validates each universe and asset field against this window. |

There is no `RiskFreeAsset` field. The risk-free rate is hard-wired to `DGS3MO` (3-month treasury yield) and resolved automatically by the engine when the data provider supplies it.

## Parameters

Exported struct fields become CLI flags via reflection. Four struct tags control behavior:

| Tag | Purpose | Example |
|-----|---------|---------|
| `pvbt` | CLI flag name. Overrides the auto-derived name. | `pvbt:"lookback"` |
| `desc` | Help text shown by `--help` and `describe`. | `desc:"Momentum lookback months"` |
| `default` | Default value if the flag is not supplied. | `default:"6"` |
| `suggest` | Named presets, pipe-delimited as `Name=value` pairs. Activated via `--preset Name`. | `suggest:"Fast=3\|Slow=12"` |

If `pvbt` is omitted, the field name is lowercased (note: this is not full kebab-case in the current code path -- `StrategyParameters` does `strings.ToLower(field.Name)`, so `RiskOn` becomes `riskon`). Always set `pvbt` explicitly when you want a kebab-case flag like `risk-on`.

Supported field types: `int`, `float64`, `string`, `bool`, `time.Duration`, `asset.Asset`, `universe.Universe`. Universe fields parse comma-separated ticker lists into a `StaticUniverse`.

All four tags on a single field:

```go
type Strategy struct {
    Lookback int `pvbt:"lookback" desc:"Momentum lookback months" default:"6" suggest:"Fast=3|Slow=12"`
}
```

The user picks a preset with `./mystrategy backtest --preset Fast`, which sets `Lookback=3`.

## What you get from cli.Run

`cli.Run(strategy)` is the conventional `main()` body. It builds a cobra command tree and registers six subcommands:

| Subcommand | Purpose |
|------------|---------|
| `backtest` | Run the strategy over a historical date range. Writes a SQLite database with the equity curve, transactions, and performance metrics. |
| `live` | Run the strategy on its declared schedule against a live broker and data provider. |
| `snapshot` | Run a backtest while capturing every data request and response into a SQLite snapshot file for offline replay tests. |
| `describe` | Print strategy metadata (parameters, defaults, presets, schedule, benchmark). Supports `--json`. |
| `study` | Parameter sweep / sensitivity analysis across the declared parameter space. |
| `config` | Inspect and validate the `pvbt.toml` configuration file (risk profiles, tax profiles, broker credentials). |

Persistent flags applied to every subcommand: `--log-level` (debug, info, warn, error) and `--config` (path to a `pvbt.toml`). Strategy-specific flags from struct tags are appended automatically.

## Read-only vs. mutable

`Compute` receives four arguments:

| Argument | Type | Mutability |
|----------|------|------------|
| `ctx` | `context.Context` | Read-only. Carries cancellation and the zerolog logger (`zerolog.Ctx(ctx)`). |
| `eng` | `*engine.Engine` | Read-only from the strategy's perspective. Use it for `eng.CurrentDate()`, `eng.Asset(ticker)`, `eng.Fetch(...)`, `eng.IndexUniverse(...)`. Do not mutate engine state inside `Compute`. |
| `portfolio` | `portfolio.Portfolio` | **Read-only query interface.** Methods like `Holdings()`, `Cash()`, `Value()`, `Position(asset)`, `MarginRatio()`, `BuyingPower()`, `ProjectedWeights()` return current state. Never write through the portfolio, even if a method appears to allow it -- this is enforced by convention, not by the type system. |
| `batch` | `*portfolio.Batch` | **The only place to write orders and annotations.** Use `batch.RebalanceTo(ctx, plan...)` for declarative target weights, or `batch.Order(ctx, asset, side, qty, modifiers...)` for imperative single-leg trades. |

The pipeline inside `Compute` is always: query state (via `portfolio` and `eng`) -> compute signals (via DataFrame math) -> write orders (via `batch`). Never mix those phases.

## See also

- [scheduling.md](./scheduling.md) -- tradecron expressions and warmup
- [universes.md](./universes.md) -- universe types and data fetching
- [portfolio-and-batch.md](./portfolio-and-batch.md) -- orders and portfolio queries
- [parameters-and-presets.md](./parameters-and-presets.md) -- CLI parameter declaration
