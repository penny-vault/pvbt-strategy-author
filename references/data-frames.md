_Last verified against pvbt 0.6.0._

# DataFrames

`data.DataFrame` is the primary type for working with time-series data inside a pvbt strategy. Universes return DataFrames from `Window` and `At`; signal and weighting functions consume them; the engine arithmetic between them is element-wise and aligned automatically.

## Shape

A DataFrame is a three-dimensional cube indexed by **(time, asset, metric)**. The `time` axis is a strictly increasing slice of `time.Time` timestamps. The `asset` axis is a slice of `asset.Asset` (each carrying its CompositeFigi identifier). The `metric` axis is a slice of `data.Metric` (price, volume, market cap, etc.).

Internally the layout is **column-major**: each `(asset, metric)` pair is stored as a single contiguous `[]float64` slice, one float per timestamp. That makes columns directly compatible with gonum and SIMD-friendly. It also means "a column" in pvbt's vocabulary is one `(asset, metric)` pair, not one timestamp. The total column count is `len(assets) * len(metrics)`.

A DataFrame may also carry auxiliary state that the engine attaches transparently: a `DataSource` reference (so downstream consumers can fetch more data) and a cumulative risk-free rate slice (so `RiskAdjustedPct` can subtract the risk-free leg). Most operations propagate this aux state automatically.

## Reading values

```go
df.Value(spy, data.MetricClose)             // most recent value (asset, metric)
df.ValueAt(spy, data.MetricClose, t)        // value at timestamp t
df.Column(spy, data.MetricClose)            // contiguous []float64, all timestamps
df.Times()                                  // copy of all timestamps as []time.Time
df.AssetList()                              // copy of all assets as []asset.Asset
df.MetricList()                             // copy of all metrics as []data.Metric

df.Len()                                    // number of timestamps
df.ColCount()                               // assets * metrics
df.Start()                                  // first timestamp
df.End()                                    // last timestamp
df.Frequency()                              // Daily, Weekly, Monthly, ...
df.Source()                                 // the DataSource that produced this DataFrame
```

`Value` returns the most recent value for the named `(asset, metric)` pair. `ValueAt` returns the value at a specific timestamp. Both return `math.NaN()` if the asset, metric, or timestamp is not present, so callers that care must check with `math.IsNaN`.

`Column` is the gonum hand-off. Because the underlying storage is contiguous, the slice it returns can be passed straight to `stat.Mean`, `stat.StdDev`, `floats.Sum`, and the rest of gonum's vector API without any copy.

`Times`, `AssetList`, and `MetricList` return defensive copies; callers may sort or mutate them freely without disturbing the DataFrame.

## Slicing

Slicing operations return a **new** DataFrame and never mutate the receiver:

```go
df.Assets(spy, tlt)                         // keep only SPY and TLT (duplicates removed)
df.Metrics(data.MetricClose)                // keep only the Close metric
df.Between(start, end)                      // keep timestamps in [start, end]
df.Last()                                   // single-row DataFrame at the most recent date
df.At(t)                                    // single-row DataFrame at timestamp t
df.Drop(math.NaN())                         // drop timestamps where any value equals the sentinel

df.Filter(func(t time.Time, row *data.DataFrame) bool {
    return row.Value(spy, data.MetricVolume) > 1_000_000
})
```

`Filter` calls the predicate once per timestamp with a single-row DataFrame containing every asset and metric at that point, so the predicate has full context. `Filter` returns a new DataFrame containing only the timestamps where the predicate returned true.

These compose naturally because each returns a `*DataFrame`:

```go
prices := df.Assets(spy).Metrics(data.MetricClose).Between(start, end)
```

If any step in the chain fails (unknown asset, unknown metric, malformed range), the failure is recorded on the result and propagated through the rest of the chain. **Always check `df.Err()` on the final result before using it**:

```go
prices := df.Assets(spy).Metrics(data.MetricClose).Last()
if err := prices.Err(); err != nil {
    return data.WithErr(fmt.Errorf("slice prices: %w", err))
}
```

## Arithmetic

Element-wise arithmetic between two DataFrames aligns by **asset and metric** (matched on CompositeFigi for assets, exact match for metrics). The two DataFrames must also share an identical timestamp axis -- pvbt does **not** auto-align timestamps and will set an error if they differ.

```go
result := df1.Add(df2)                      // element-wise addition
result := df1.Sub(df2)                      // element-wise subtraction
result := df1.Mul(df2)                      // element-wise multiplication
result := df1.Div(df2)                      // element-wise division
```

The result contains only the `(asset, metric)` pairs present in **both** inputs; columns missing on either side are silently dropped from the result. If the intersection is empty the result is an empty DataFrame.

When the right-hand side is a single-metric helper that should apply to every metric on the left, pass the metric name to broadcast:

```go
// Divide every metric in df by Close from priceDF, asset-by-asset.
normalized := df.Div(priceDF, data.MetricClose)
```

Scalar arithmetic applies a constant to every value in the DataFrame:

```go
df.AddScalar(1.0)
df.SubScalar(0.5)
df.MulScalar(0.5)
df.DivScalar(100.0)
```

Scalar operations only depend on the receiver, so they cannot fail on alignment.

## Financial calculations

Each of these returns a new DataFrame with the same shape (time, asset, metric) as the receiver. Operations that need a previous value fill leading positions with `math.NaN()`.

| Method | Semantics |
|---|---|
| `df.Pct()` / `df.Pct(n)` | Percent change over `n` periods: `(x[t] - x[t-n]) / x[t-n]`. Default `n` is 1. |
| `df.RiskAdjustedPct()` / `df.RiskAdjustedPct(n)` | Same as `Pct(n)` minus the risk-free return accumulated over the same `n` periods. Requires risk-free rates attached; see below. |
| `df.Diff()` | First difference: `x[t] - x[t-1]`. |
| `df.Log()` | Natural logarithm of every value. |
| `df.CumSum()` | Cumulative sum along the time axis for each column. |
| `df.CumMax()` | Running maximum along the time axis for each column. |
| `df.Shift(n)` | Shift every column forward by `n` periods (NaN-filled). Negative `n` shifts backward. |
| `df.Covariance(assets...)` | Sample covariance (N-1 denominator). With one asset, returns cross-metric covariance with composite metric keys. With two or more assets, returns per-metric covariance for all unique pairs with composite asset keys. |

Companion methods `df.Correlation(assets...)` (Pearson correlation) and `df.Std()` / `df.Variance()` (column-wise standard deviation and variance over the time dimension) are also available.

## Aggregations

Aggregations come in two flavors depending on which dimension they collapse.

**Reduce the time dimension** (one value per column, returned as a single-row DataFrame):

| Method | Semantics |
|---|---|
| `df.Mean()` | Arithmetic mean of each column over time. |
| `df.Sum()` | Sum of each column over time. |
| `df.Max()` | Maximum of each column over time. |
| `df.Min()` | Minimum of each column over time. |
| `df.Variance()` | Sample variance (N-1 denominator) of each column over time. |
| `df.Std()` | Sample standard deviation (N-1 denominator) of each column over time. |

These all preserve the asset and metric axes and return a DataFrame with `Len() == 1`.

**Reduce the asset dimension** (one value per timestamp per metric, returned as a multi-row, single-asset DataFrame whose ticker is a synthetic placeholder):

| Method | Semantics |
|---|---|
| `df.MaxAcrossAssets()` | Per timestamp, the maximum value across assets for each metric. The result has a single synthetic asset with `Ticker == "MAX"`. |
| `df.MinAcrossAssets()` | Per timestamp, the minimum value across assets for each metric. Synthetic ticker `"MIN"`. |
| `df.IdxMaxAcrossAssets()` | Per timestamp, the asset that holds the maximum for the **first** metric. Returns `[]asset.Asset`, **not** a DataFrame. |
| `df.CountWhere(metric, predicate)` | Per timestamp, the number of assets where `predicate(value)` is true for `metric`. Synthetic ticker `"COUNT"`, single metric `Count`. |

The cross-asset reductions keep every timestamp and every metric; only the asset axis collapses. The synthetic ticker is a deliberate convention so callers can distinguish a reduced DataFrame from one that happens to contain a single real asset.

## Rolling windows

`df.Rolling(n)` returns a builder. Call an aggregation method on it to materialize the rolling result as a new DataFrame with the same shape as the receiver:

```go
sma20 := df.Metrics(data.MetricClose).Rolling(20).Mean()      // 20-period SMA
volatility := df.Metrics(data.MetricClose).Rolling(60).Std()  // 60-period rolling stdev
```

Available aggregations on a `RollingDataFrame`:

- `Mean()` -- simple moving average
- `Sum()` -- rolling sum
- `Max()` -- rolling maximum
- `Min()` -- rolling minimum
- `Std()` -- rolling sample standard deviation (N-1)
- `Variance()` -- rolling sample variance (N-1)
- `Percentile(p)` -- rolling p-th percentile, `p` in `[0, 1]`
- `EMA()` -- exponential moving average with `alpha = 2/(n+1)`

The first `n-1` positions are NaN-filled because the window is not yet full.

## Resampling

Resampling changes the frequency of the time axis.

**Downsample** -- aggregate values inside each lower-frequency bucket. `df.Downsample(freq)` returns a builder; call an aggregation method to materialize:

```go
df.Downsample(data.Weekly).Last()           // weekly close
df.Downsample(data.Weekly).Sum()            // weekly total volume
df.Downsample(data.Monthly).Max()           // monthly high
df.Downsample(data.Monthly).First()         // monthly open
```

Available downsample aggregations: `Mean()`, `Sum()`, `Max()`, `Min()`, `First()`, `Last()`, `Std()`, `Variance()`. OHLC bars are not a primitive -- they are the convention `Open=First, High=Max, Low=Min, Close=Last`.

**Upsample** -- fill the gaps when going to a higher frequency:

```go
df.Upsample(data.Daily).ForwardFill()       // carry the previous value forward
df.Upsample(data.Daily).BackFill()          // pull the next value backward
df.Upsample(data.Daily).Interpolate()       // linear interpolation between known points
```

## Error propagation

DataFrame operations return new DataFrames; the receiver is never mutated by the slicing, arithmetic, financial-calculation, aggregation, rolling, or resampling families above. (A small number of construction-time methods -- `AppendRow`, `Insert`, `RenameMetric` -- mutate in place and are not part of the analysis API.)

When something goes wrong (alignment mismatch, unknown metric, missing risk-free rates, empty result), the operation returns a DataFrame whose `Err()` is non-nil. Subsequent operations on that DataFrame are no-ops -- they short-circuit and propagate the same error to their result. This means a long chain can fail at any step and the failure surfaces only at the final `Err()` check:

```go
result := df.Assets(spy).Metrics(data.MetricClose).Pct(20).Rolling(60).Mean()
if err := result.Err(); err != nil {
    return data.WithErr(fmt.Errorf("compute 60-day rolling mean of 20-day momentum: %w", err))
}
```

The convention inside signal functions is to return early with `data.WithErr(err)` so the caller's `Err()` check sees the wrapped context. Never silently swallow `Err()` -- a result with an error and no data will produce NaN-filled or empty downstream behavior that is hard to diagnose later.

## When to use RiskAdjustedPct vs. Pct

`Pct(n)` is raw percent change. `RiskAdjustedPct(n)` is the same percent change with the risk-free return over the same `n`-period window subtracted off.

Use `RiskAdjustedPct` when the signal **should not reward an asset just for sitting in a high-rate environment**. Two concrete cases:

- **Cross-sectional momentum.** Ranking assets by 12-month return without subtracting the risk-free leg lets cash-like instruments and short-duration treasuries score high in periods when short rates are high, even though they carry no real excess return. `RiskAdjustedPct(252)` removes that bias.
- **Comparing risk-on to risk-off.** A regime detector that compares equity returns to bond returns will get the wrong answer when the risk-free rate alone explains the bond leg. Subtracting the risk-free return puts both legs on the same excess-return footing.

Use plain `Pct` when the signal is about **price dynamics for their own sake** -- volatility, drawdown, breakouts, or any computation where the absolute path matters and the risk-free leg would just add noise.

`RiskAdjustedPct` requires risk-free rates to be attached to the DataFrame. The engine attaches cumulative DGS3MO data automatically when a risk-free asset is configured (DGS3MO is the default). If no risk-free rates are present, `RiskAdjustedPct` returns a DataFrame with an error -- it does **not** silently fall back to plain `Pct`. Strategies that rely on `RiskAdjustedPct` should ensure the engine has a working FRED data source.

## See also

- [./signals-and-weighting.md](./signals-and-weighting.md) -- how DataFrames flow through signal functions and into weighting decisions.
- [./portfolio-and-batch.md](./portfolio-and-batch.md) -- the read-only `Portfolio` queries and `Batch` order interface that consume DataFrame-derived signals.
