_Last verified against pvbt 0.6.0._

# Universes

## What a universe is

A universe is the time-varying set of assets a strategy can observe and trade, plus the data-fetching methods (`Window`, `At`) used to pull market data for those assets at the current simulation date.

## The Universe interface

```go
package universe

type Universe interface {
    Assets(t time.Time) []asset.Asset
    Window(ctx context.Context, lookback portfolio.Period, metrics ...data.Metric) (*data.DataFrame, error)
    At(ctx context.Context, metrics ...data.Metric) (*data.DataFrame, error)
    CurrentDate() time.Time
}
```

- `Assets(t)` -- members at simulation date `t`. The engine calls this at each computation step.
- `Window(ctx, lookback, metrics...)` -- DataFrame covering `[currentDate - lookback, currentDate]` for the universe's current members.
- `At(ctx, metrics...)` -- single-row DataFrame at `currentDate` for the universe's current members.
- `CurrentDate()` -- the engine's current simulation date.

A universe is only useful once it is wired to a data source. Universes constructed via the `engine.Engine` helpers (`eng.Universe`, `eng.IndexUniverse`, `eng.RatedUniverse`) and universes auto-built from struct tags are wired automatically. A bare `universe.NewStatic(...)` returned without engine wiring will return an error from `Window`/`At` until it is bound. The canonical wiring path inside `Setup` is `s.myUniverse = eng.Universe(eng.Asset("SPY"), eng.Asset("EFA"))`.

## Kinds

### Static

A `StaticUniverse` is a fixed list of assets whose membership does not change with the simulation date. Static universes are the most common form and have two construction styles.

**As a CLI-exposed struct field.** Declare an exported `universe.Universe` field with `pvbt`, `desc`, and `default` struct tags. The engine uses reflection to find the field, parses the comma-separated `default` value into a `StaticUniverse`, and wires it to the engine before `Setup` runs. The `pvbt` tag becomes the CLI flag name; users can override the asset list with `--<flag> SPY,QQQ,IWM`.

```go
type ADM struct {
    RiskOn  universe.Universe `pvbt:"risk-on"  desc:"Assets to rotate between" default:"SPY,EFA,EEM"`
    RiskOff universe.Universe `pvbt:"risk-off" desc:"Safe-haven asset"         default:"SHY"`
}
```

This is the right form whenever the asset list is something a user might reasonably want to change at the command line.

**Imperatively in `Setup`.** Build the universe from explicit tickers when it should be hard-coded and not user-configurable. Use the engine helper so wiring is automatic.

```go
func (s *Hedged) Setup(eng *engine.Engine) {
    s.hedges = eng.Universe(eng.Asset("GLD"), eng.Asset("TLT"))
}
```

`universe.NewStatic("GLD", "TLT")` is the lower-level constructor. It produces an unwired `StaticUniverse` that must be bound to a data source separately, so prefer `eng.Universe(...)` from inside `Setup`.

Tickers may carry a namespace prefix to select a non-default data source: `eng.Asset("FRED:DGS10")` or `universe.NewStatic("FRED:DGS3MO", "FRED:DGS10")`.

### Index-tracking

An index universe resolves its membership from an `IndexProvider` at every simulation step. Membership changes over time, mirroring the historical composition of the underlying index, which prevents survivorship bias.

```go
func (s *MyStrategy) Setup(eng *engine.Engine) {
    s.sp500 = eng.IndexUniverse("SPX")
    s.ndx   = eng.IndexUniverse("NDX")
}
```

`eng.IndexUniverse(name)` finds the registered `IndexProvider`, constructs an `indexUniverse`, and wires it to the engine's data source. The provider loads snapshot and changelog data on first access and advances monotonically; the slice returned by `Assets(t)` is borrowed from the provider and is only valid for the current engine step. Strategies that need to retain membership across steps must copy the slice.

Supported index codes are determined by what the registered `IndexProvider` knows how to serve. The codes pvbt's `pv-data` provider exposes today are:

- `SPX` -- S&P 500
- `NDX` -- Nasdaq 100
- `us-tradable` -- the curated daily-refreshed liquid US common-stock universe (see below)

Two convenience constructors live alongside the engine helper for use outside `Setup` (for example, in tests with a hand-built provider): `universe.SP500(provider)` and `universe.Nasdaq100(provider)`. Both delegate to `universe.NewIndex(provider, "SPX")` / `("NDX")`.

### USTradable convenience

`universe.USTradable(provider)` and the equivalent `eng.IndexUniverse("us-tradable")` return the recommended default universe for any broad US equity strategy. It is recomputed daily by `pv-data` and contains only US common stocks meeting all of the following criteria:

- Market cap above the 25th percentile of US-listed common stocks (a percentile floor that adapts across time, not a fixed dollar threshold).
- Median daily dollar volume of at least $2.5M over the trailing 200 days.
- Prior close of at least $5.
- 200 trading days of contiguous price and volume history.

ADRs, limited partnerships, ETFs, closed-end funds, OTC stocks, and recent IPOs are excluded. For companies with multiple share classes, only the most liquid common share is kept. Membership typically lands in the 1,500-2,500 range. These are the same constraints Quantopian's `QTradableStocksUS` used.

When to reach for it:

- Use `USTradable` (or `eng.IndexUniverse("us-tradable")`) as the default for any factor, momentum, value, mean-reversion, or cross-sectional strategy operating on broad US equities.
- Use `SPX` or `NDX` only when the strategy specifically tracks those index compositions (e.g., an SPX-constituent rotation).
- Use `NewStatic` / `eng.Universe` only for fixed asset rotations such as ETF tactical allocation strategies.

```go
func (s *MomentumStrategy) Setup(eng *engine.Engine) {
    s.stocks = eng.IndexUniverse("us-tradable")
}
```

### Rated

A `ratedUniverse` resolves membership from a `RatingProvider` plus a `data.RatingFilter`. Membership at any date is "all assets the named analyst rated such that the filter returns true." Use this when the investable set is defined by an external rating signal (e.g. Morningstar five-star stocks, Zacks Rank 1).

```go
func (s *FiveStars) Setup(eng *engine.Engine) {
    s.buys = eng.RatedUniverse("morningstar", data.RatingEq(5))
}
```

`eng.RatedUniverse(analyst, filter)` finds a registered `RatingProvider`, constructs a `ratedUniverse`, and wires it to the engine. Results are cached in memory keyed by date, so repeated calls within a step are cheap. Common filter helpers include `data.RatingEq`, `data.RatingGte`, and `data.RatingLte`. The lower-level `universe.NewRated(provider, analyst, filter)` is available for tests.

## Fetching data

Both `Window` and `At` use the universe's **current** simulation date as the anchor; neither takes a date argument. The engine guarantees that `currentDate` matches the tick that triggered `Compute`.

### Window

```go
Window(ctx context.Context, lookback portfolio.Period, metrics ...data.Metric) (*data.DataFrame, error)
```

Returns a DataFrame spanning `[currentDate - lookback, currentDate]` (inclusive on both ends, subject to trading-day filtering) with one column per `(asset, metric)` pair. This is the primary path for signal computation.

```go
df, err := s.stocks.Window(ctx, portfolio.Months(6), data.MetricClose, data.MetricVolume)
if err != nil {
    return fmt.Errorf("fetch window: %w", err)
}
```

For an index or rated universe, members are re-resolved at the moment of the call: `Window` asks the provider for `Assets(currentDate)` and then fetches the full lookback history for that membership. Only assets that are members **as of `currentDate`** appear in the result; assets that were dropped from the index earlier in the lookback window are excluded, and assets that were added during the window have their full history fetched (including the dates before they joined the index).

### At

```go
At(ctx context.Context, metrics ...data.Metric) (*data.DataFrame, error)
```

Returns a single-row DataFrame at `currentDate` for the same membership. Use it when only the latest value of each metric matters (e.g., reading the latest closing price to size positions).

```go
row, err := s.stocks.At(ctx, data.MetricClose)
if err != nil {
    return fmt.Errorf("fetch latest close: %w", err)
}
latestPrices := row // single-row DataFrame; index by (asset, metric)
```

`At` is equivalent to `Window(ctx, portfolio.Days(0), metrics...).Last()`, but cheaper because the engine fetches one row instead of a window.

## Survivorship bias

A backtest is survivorship-biased when its universe at a historical date contains only assets that **survived to the present**. The classic example: backtesting a "buy the S&P 500" strategy against today's S&P 500 list applied to 2008 data. That list excludes Lehman, Wachovia, Bear Stearns, Washington Mutual, Circuit City, and dozens of other companies that were S&P 500 members in 2008 but are gone now. The backtest never gets a chance to lose money on them, so reported returns are systematically inflated and reported drawdowns are systematically understated.

Static universes built from a hand-curated ticker list are dangerous in long backtests for exactly this reason: they cannot include assets that were delisted, acquired, or went bankrupt before today. The longer the backtest, the larger the bias.

The fix is index universes (`eng.IndexUniverse("SPX")`, `eng.IndexUniverse("us-tradable")`). These resolve membership against the historical snapshot/changelog tables on every simulation step. On 2008-09-15, the universe contains Lehman; on 2008-09-16, it does not. The strategy sees the same investable set the historical investor would have seen.

Static universes are still appropriate when:

- The asset list is intentionally fixed and well-known (a sector ETF rotation, a treasury ladder, a small set of macro instruments).
- The backtest window is short enough that survivorship effects are negligible.
- Every asset in the list existed for the full backtest period and remains tradeable.

Outside those cases, prefer an index universe -- typically `us-tradable` for broad US equity strategies.

## See also

- [./data-frames.md](./data-frames.md) -- shape, indexing, and operations on the DataFrames returned by `Window` and `At`.
- [./common-pitfalls.md](./common-pitfalls.md) -- broader catalog of mistakes when writing pvbt strategies, including survivorship and lookahead bias.
