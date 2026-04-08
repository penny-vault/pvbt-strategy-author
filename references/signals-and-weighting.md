_Last verified against pvbt 0.6.0._

# Signals and Weighting

A signal is any DataFrame whose values rank or score assets at a point in time. A selector turns those scores into a Boolean choice (which assets to hold). A weighter turns the chosen set into a `PortfolioPlan` with target weights. The three stages are loosely coupled: any DataFrame that has a `Selected` column can be fed to any weighter, regardless of how the column got there.

The `signal` package ships a long list of pre-built scoring functions (`Momentum`, `RSI`, `Volatility`, `MACD`, `BollingerBands`, `ZScore`, `EarningsYield`, etc.). Each is a plain function that takes a `context.Context` and a `universe.Universe` and returns a `*data.DataFrame`. Custom signals are also plain functions -- there is no interface to implement. See `pvbt/docs/signals.md` for the full catalog.

## Pipeline

```
fetch data        ->   *data.DataFrame
score / signal    ->   *data.DataFrame   (scores per asset per timestep)
selector.Select   ->   same *data.DataFrame, now with a "selected" column
weighter          ->   portfolio.PortfolioPlan
batch.RebalanceTo ->   orders queued for the engine
```

The selector mutates its input DataFrame in place and returns the same pointer. The weighter reads the `Selected` column (constant `portfolio.Selected = "selected"`) and produces an allocation per timestep. The batch executes the plan against current holdings.

Canonical idiom:

```go
df, err := u.Window(ctx, portfolio.Months(12), data.AdjClose)
if err != nil {
    return err
}

scored := df.Pct(df.Len() - 1).Last() // 12-month return as a score

portfolio.TopN(3, data.AdjClose).Select(scored) // mutates scored in place

plan, err := portfolio.EqualWeight(scored)
if err != nil {
    return err
}

return batch.RebalanceTo(ctx, plan...)
```

The `Select` call is an expression-statement: its return value is the same pointer as the input, so the receiver variable is unchanged. Treat selectors as side-effecting and call them for the mutation, not the return value.

## Built-in selectors

All selectors satisfy the `portfolio.Selector` interface:

```go
type Selector interface {
    Select(df *data.DataFrame) *data.DataFrame
}
```

`Select` inserts a `Selected` metric column (`portfolio.Selected`, value `"selected"`). Cells where the asset is chosen at that timestep get `1.0`; cells where it is not get `0.0`. The `Selected` column supports fractional values for future selectors but every built-in produces only `0.0` or `1.0`. The DataFrame is mutated in place and the same pointer is returned. Insert errors inside the selector are logged via zerolog and not returned, because the interface has no error channel.

### MaxAboveZero

```go
func MaxAboveZero(metric data.Metric, fallback *data.DataFrame) Selector
```

At each timestep picks the single asset with the highest **strictly positive** value in `metric`. Ties are broken by iteration order over the asset list.

If no asset has a positive value at a timestep:

- If `fallback` is `nil`, no asset is selected at that timestep.
- If `fallback` is non-nil, every asset and metric column from `fallback` is inserted into `df` (overwriting nothing) and every fallback asset is marked selected at that timestep.

The fallback DataFrame must share the same time index as `df`. A mismatched index produces zerolog warnings but does not panic.

```go
// Pick the single best momentum asset; hold cash (BIL) when nothing is positive.
cash, _ := u.Window(ctx, portfolio.Days(1), data.AdjClose) // BIL series
mom := signal.Momentum(ctx, u, portfolio.Months(6))
portfolio.MaxAboveZero(signal.MomentumSignal, cash).Select(mom)
plan, err := portfolio.EqualWeight(mom)
```

This is the dual-momentum / accelerating-dual-momentum pattern: momentum picks the winner, and the fallback DataFrame represents the "go to cash" branch when nothing has positive momentum.

### TopN, BottomN

```go
func TopN(count int, metric data.Metric) Selector
func BottomN(count int, metric data.Metric) Selector
```

`TopN` marks the `count` assets with the **highest** values in `metric` at each timestep. `BottomN` marks the `count` lowest. Both panic if `count < 1`. NaN values are excluded from the ranking before sorting; if fewer than `count` non-NaN values are available, all of them are selected.

```go
// Top 5 momentum names.
df := signal.Momentum(ctx, u, portfolio.Months(12))
portfolio.TopN(5, signal.MomentumSignal).Select(df)

// Cheapest 10 names by P/E.
df := signal.EarningsYield(ctx, u) // higher EY = cheaper
portfolio.BottomN(10, signal.EarningsYieldSignal).Select(df)
```

### CountWhere

`CountWhere` is not a `Selector`. It is a method on `*data.DataFrame` that produces a single-asset DataFrame containing one metric (`data.Count`) whose value at each timestep is the number of assets matching a predicate. It is the standard tool for canary-style signals -- "hold equities only if at least N of these N tickers are positive over the lookback window".

```go
func (df *DataFrame) CountWhere(metric Metric, predicate func(float64) bool) *DataFrame
```

The synthetic asset has `Ticker: "COUNT"`. NaN-aware predicates are required because `Pct()` produces NaN at the start of every column.

```go
// Canary: how many of the canary tickers are positive over the past month?
canaryDF, _ := canary.Window(ctx, portfolio.Months(1), data.AdjClose)
returns := canaryDF.Pct(canaryDF.Len() - 1).Last()

positiveCount := returns.CountWhere(data.AdjClose, func(value float64) bool {
    return !math.IsNaN(value) && value > 0
})

// Branch on the synthetic Count value at the latest timestep.
nPositive := positiveCount.ValueAt(asset.Asset{Ticker: "COUNT"}, data.Count, eng.CurrentDate())
if nPositive >= 2 {
    // risk-on branch
} else {
    // risk-off branch
}
```

## Built-in weighting

Every weighter reads the `Selected` column and returns `(PortfolioPlan, error)`. They all error if the column is missing (`ErrMissingSelected`). Several weighters need price data and will fetch it via `df.Source()` if it is not already in the DataFrame; those error if the DataFrame has no attached source (`ErrNoDataSource`).

### EqualWeight

```go
func EqualWeight(df *data.DataFrame) (PortfolioPlan, error)
```

Assigns `1/N` to each asset where `Selected > 0` at each timestep. Magnitude of the `Selected` value is ignored. If no asset is selected at a timestep, that allocation has an empty `Members` map (no positions held).

```go
plan, err := portfolio.EqualWeight(df)
if err != nil {
    return err
}
return batch.RebalanceTo(ctx, plan...)
```

### WeightedBySignal

```go
func WeightedBySignal(df *data.DataFrame, metric data.Metric) (PortfolioPlan, error)
```

Weights selected assets proportionally to the values in `metric`. Zero, NaN, and negative values are discarded; weights are normalized to sum to 1.0. If every selected asset has a non-positive metric value at a timestep, falls back to equal weight among the selected assets.

```go
// Selected assets weighted by raw momentum score.
plan, err := portfolio.WeightedBySignal(df, signal.MomentumSignal)
```

### MarketCapWeighted

```go
func MarketCapWeighted(ctx context.Context, df *data.DataFrame) (PortfolioPlan, error)
```

Weights each selected asset proportionally to `data.MarketCap`. Fetches market cap via the DataFrame's data source if not present. Falls back to equal weight when all selected assets have zero or NaN market caps.

### InverseVolatility

```go
func InverseVolatility(ctx context.Context, df *data.DataFrame, lookback data.Period) (PortfolioPlan, error)
```

Weights inversely proportional to trailing standard deviation of returns. A zero-value lookback (`data.Period{}`) defaults to 60 calendar days. Fetches `AdjClose` if not already present. Falls back to equal weight when every selected asset has zero or NaN volatility, or when only one asset is selected.

```go
plan, err := portfolio.InverseVolatility(ctx, df, portfolio.Days(60))
```

### RiskParity

```go
func RiskParity(ctx context.Context, df *data.DataFrame, lookback data.Period) (PortfolioPlan, error)
```

Iterative equal-risk-contribution solver using Newton's method with simplex projection. Logs a zerolog warning if convergence is not reached within `riskParityMaxIter` iterations and returns the best result found. Falls back to equal weight when the covariance matrix degenerates or only one asset is selected. Default lookback is 60 calendar days. Strictly more accurate than `RiskParityFast` and strictly slower.

### RiskParityFast

```go
func RiskParityFast(ctx context.Context, df *data.DataFrame, lookback data.Period) (PortfolioPlan, error)
```

Single-pass approximation of equal risk contribution. Starts from inverse-volatility weights and adjusts once for pairwise correlations. Falls back to equal weight on degeneracy. Use this when iterative `RiskParity` is too slow and you can tolerate a less precise solution.

## Writing a custom signal

The built-ins cover the common cases. When you need a metric they do not provide -- a custom valuation ratio, a sentiment score, a regime indicator -- write a plain function that returns a `*data.DataFrame`. There is no interface and no registration step.

**Reuse DataFrame methods rather than raw loops.** Signal code that loops over `df.Times()` and calls `df.ValueAt` for each cell is almost always slower and less correct than the equivalent built-in transform. The DataFrame API is designed to compose: `Pct`, `Diff`, `Rolling`, `Std`, `Mean`, `Apply`, plus arithmetic (`Add`, `Sub`, `Mul`, `Div`, `DivScalar`) all preserve alignment by `(asset, metric, timestamp)`.

Example: a custom z-score of book-to-price.

```go
func BookToPriceZScore(ctx context.Context, u universe.Universe, lookback portfolio.Period) *data.DataFrame {
    df, err := u.Window(ctx, lookback, data.BookValue, data.MetricClose)
    if err != nil {
        return data.WithErr(err)
    }

    book := df.Metrics(data.BookValue)
    price := df.Metrics(data.MetricClose)
    bp := book.Div(price)

    rollingMean := bp.Rolling(lookback.N).Mean()
    rollingStd := bp.Rolling(lookback.N).Std()

    return bp.Sub(rollingMean).Div(rollingStd)
}
```

`data.WithErr` returns an error-carrying DataFrame so callers can check `df.Err()` uniformly.

## Recommended idioms

These idioms compose the built-ins efficiently. They are the patterns the built-in `signal` package uses internally.

**N-period total return (momentum):**

```go
mom := df.Pct(n).Last()
```

`Pct(n)` produces the percent change over an `n`-period window for every column; `Last()` collapses to a single-row DataFrame containing the most recent value. The result is a one-row DataFrame keyed by `(asset, metric)` and is ready to feed into a selector.

**Risk-adjusted return (momentum minus risk-free):**

```go
rar := df.RiskAdjustedPct(n).Last()
```

`RiskAdjustedPct` requires risk-free rates attached to the DataFrame (the engine attaches them automatically when a risk-free asset is configured). It returns the percent change minus the matched-period risk-free return.

**Smoothed rank (rolling-mean rank):**

```go
smoothed := df.Rolling(n).Mean()
portfolio.TopN(5, data.AdjClose).Select(smoothed)
```

This ranks by an `n`-period rolling average rather than a single noisy reading.

**Volatility:**

```go
vol := df.Pct().Rolling(n).Std()
```

Daily returns first, then a rolling standard deviation. Use `signal.Volatility` directly if the universe is the right shape -- it does the same thing.

## Lookback helpers

Lookback windows show up in two places: when fetching data from a universe (`u.Window(ctx, lookback, ...)`) and when configuring weighters that need a trailing window (`InverseVolatility`, `RiskParity`).

**Prefer `portfolio.Months(n)` and `portfolio.Days(n)` over raw integers.** They produce `data.Period` values that the data layer interprets as calendar months / calendar days, which respect month-end boundaries and avoid the off-by-one errors common to "21 trading days = 1 month" approximations. `portfolio.Months` is an alias for `data.Months`; either name works.

```go
// Good: explicit calendar semantics, matches built-in signal conventions.
df, _ := u.Window(ctx, portfolio.Months(12), data.AdjClose)

// Avoid: raw integer days drift relative to month boundaries.
df, _ := u.Window(ctx, portfolio.Days(252), data.AdjClose)
```

Use `portfolio.Days(n)` only when the window is intrinsically a number of trading bars (e.g. a 14-day RSI). Other helpers: `portfolio.Years`, `portfolio.YTD`, `portfolio.MTD`, `portfolio.WTD`.

## Combining metrics

DataFrame arithmetic is aligned by `(asset, metric, timestamp)`, so combining signals is just arithmetic. The combined DataFrame is itself a valid signal -- feed it to any selector.

```go
mom := signal.Momentum(ctx, u, portfolio.Months(12))      // higher = better
vol := signal.Volatility(ctx, u, portfolio.Days(60))      // higher = worse

// 60% momentum minus 40% volatility, normalized.
score := mom.MulScalar(0.6).Sub(vol.MulScalar(0.4))

portfolio.TopN(5, signal.MomentumSignal).Select(score)
plan, err := portfolio.EqualWeight(score)
```

When the operands have different metric names (as above), arithmetic operations preserve the left-hand operand's metric name in the result. Pass that name to the selector.

For composite scores across many lookback windows, average several `Pct(n)` outputs:

```go
mom3 := df.Pct(63).Last()
mom6 := df.Pct(126).Last()
mom12 := df.Pct(252).Last()
composite := mom3.Add(mom6).Add(mom12).DivScalar(3)
```

Errors propagate through chained arithmetic, so a single `composite.Err()` check at the end is sufficient.

## See also

- [./data-frames.md](./data-frames.md) -- DataFrame construction, querying, transforms, arithmetic, and the cross-asset reduction methods (`MaxAcrossAssets`, `MinAcrossAssets`, etc.).
- [./portfolio-and-batch.md](./portfolio-and-batch.md) -- how `PortfolioPlan` flows through `batch.RebalanceTo` and the middleware chain to become broker orders.
