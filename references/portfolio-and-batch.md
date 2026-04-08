_Last verified against pvbt 0.6.0._

# Portfolio and Batch

`Compute` receives two trading-related arguments: a `portfolio.Portfolio` for reading current state and a `*portfolio.Batch` for writing orders and annotations. The split is enforced by the type system and by engine convention -- the portfolio has no mutation methods, and the batch has no public field that lets a strategy write directly to portfolio state. Every order accumulated in the batch is processed by the middleware chain after `Compute` returns and only then submitted to the broker.

## Portfolio (read-only)

`portfolio.Portfolio` is the interface in `portfolio/portfolio.go`. The methods most strategies need:

| Method | Returns |
|--------|---------|
| `Cash() float64` | Available cash balance. |
| `Value() float64` | Total portfolio value: cash plus all holdings marked to current prices. |
| `Equity() float64` | Cash plus long market value minus short market value. |
| `Position(ast asset.Asset) float64` | Quantity held of a specific asset. Negative for shorts. Zero if not held. |
| `PositionValue(ast asset.Asset) float64` | Mark-to-market dollar value of a position. Negative for shorts. |
| `Holdings() map[asset.Asset]float64` | Map of every current position keyed by asset. Quantities are negative for shorts. |
| `LongMarketValue() float64` | Total market value of long positions only. |
| `ShortMarketValue() float64` | Total absolute market value of short positions (always >= 0). |
| `MarginRatio() float64` | Equity divided by short market value. NaN if no shorts. |
| `MarginDeficiency() float64` | Dollars below the maintenance margin threshold. 0 if compliant or no shorts. |
| `BuyingPower() float64` | Cash minus the initial margin reserved by existing shorts. |
| `Prices() *data.DataFrame` | Most recent price DataFrame for held + benchmark + risk-free assets. May be nil before the first update. |
| `Transactions() []Transaction` | Full transaction log in chronological order. |
| `Benchmark() asset.Asset` | The asset configured as the benchmark. |
| `Annotations() []Annotation` | Full annotation log. |
| `View(start, end time.Time) Portfolio` | Return a windowed view bound to a date range. |

`ProjectedWeights() map[asset.Asset]float64` lives on `*Batch`, not on `Portfolio`. It returns position weights as they would be after the orders queued in the current batch execute at last-known prices. Use it after writing orders to verify the resulting allocation.

`Portfolio` also exposes performance/risk/tax/trade metric accessors (`Summary`, `RiskMetrics`, `TaxMetrics`, `TradeMetrics`, `WithdrawalMetrics`, `PerformanceMetric(...)`, `FactorAnalysis(...)`, etc.). These are useful inside `Compute` for adaptive strategies that branch on past performance. Single-character method names (`Cash`, `Value`) are documented at `portfolio/portfolio.go`.

## Batch

`*portfolio.Batch` (in `portfolio/batch.go`) accumulates orders and annotations during a single `Compute` call. Two ways to write to it:

- **Declarative:** `batch.RebalanceTo(ctx, allocs...)` -- diff current holdings against a target allocation.
- **Imperative:** `batch.Order(ctx, asset, side, qty, mods...)` -- queue a specific order with modifiers.

Both paths populate the same `Orders` slice. The two approaches can be mixed freely on a single batch. After `Compute` returns the engine runs the full batch through the middleware chain and submits surviving orders to the broker.

`batch.Annotate(key, value)` records a key-value entry that the engine timestamps with the current frame date. `df.Annotate(batch)` is the convenience helper that decomposes a DataFrame into "TICKER/Metric" entries.

Useful query methods on `Batch`:

| Method | Returns |
|--------|---------|
| `Portfolio() Portfolio` | The read-only portfolio reference the batch is bound to. |
| `ProjectedHoldings() map[asset.Asset]float64` | Holdings after every queued order executes at last-known prices. |
| `ProjectedValue() float64` | Total portfolio value after every queued order executes. |
| `ProjectedWeights() map[asset.Asset]float64` | Position weights after every queued order executes. |
| `Groups() []OrderGroupSpec` | Bracket and OCO group descriptors collected so far. |

**Never write to `Portfolio` from inside `Compute`.** The engine creates a fresh batch for each frame, runs `Compute`, then drains the batch through middleware and submits orders. Strategy code does not call `Account.RebalanceTo`, `Account.Order`, or `Account.Record` directly -- those mutate the account immediately and bypass the batch lifecycle. Strategy authors only ever see the `Portfolio` interface, which has no mutation methods.

## Declarative: RebalanceTo

The most common construction pattern is a three-step pipeline: **select** which assets to hold, **weight** them into a plan, **execute** the plan against the batch.

```go
// 1. Select -- mutate the DataFrame in place to mark chosen assets.
portfolio.TopN(3, data.MetricClose).Select(momentum)

// 2. Weight -- produce a PortfolioPlan with target weights.
plan, err := portfolio.EqualWeight(momentum)
if err != nil {
    return err
}

// 3. Execute -- queue the orders needed to reach the plan.
return batch.RebalanceTo(ctx, plan...)
```

`RebalanceTo` accepts variadic `Allocation` arguments -- pass a single allocation for an immediate rebalance or spread a `PortfolioPlan` to apply a series of allocations in date order. It diffs current holdings against each target, sells overweight positions, covers any shorts not in the new target, and then buys to fill the rest. Cash is the implicit remainder.

Common selectors (full reference in `signals-and-weighting.md`):

```go
// Single best asset by metric, with a fallback DataFrame for risk-off.
portfolio.MaxAboveZero(data.MetricClose, riskOffDF).Select(momentum)

// Top N by metric.
portfolio.TopN(3, data.MetricClose).Select(momentum)

// Bottom N by metric.
portfolio.BottomN(2, data.PE).Select(valuations)

// Count assets matching a predicate at each timestep.
badCanary := protectiveMom.CountWhere(data.AdjClose, func(v float64) bool {
    return math.IsNaN(v) || v <= 0
})
```

Selectors mutate the input DataFrame in place by inserting a `Selected` metric column and return the same pointer.

`portfolio.EqualWeight(df)` is the simplest weighting function: every asset where `Selected > 0` receives the same weight. Other built-ins (`WeightedBySignal`, `InverseVolatility`, `MarketCapWeighted`, `RiskParityFast`, `RiskParity`) are documented in `signals-and-weighting.md`.

### Allocation

```go
type Allocation struct {
    Date          time.Time
    Members       map[asset.Asset]float64
    Justification string // optional; copied onto every Transaction generated by RebalanceTo
}

type PortfolioPlan []Allocation
```

`Members` weights are interpreted as fractions of total portfolio value:

- For long-only allocations the weights typically sum to 1.0. Anything left over is held as cash.
- The sum of absolute weights can exceed 1.0 to express leverage; gross exposure beyond 1.0 requires margin.
- Negative weights open or maintain short positions (see [Short selling](#short-selling)).
- A `$CASH` member is filtered out -- cash is the implicit remainder.

`Justification` is optional. When set, every `Transaction` produced for the allocation copies the string for downstream auditing.

## Imperative: Order

```go
func (b *Batch) Order(ctx context.Context, ast asset.Asset, side Side, qty float64, mods ...OrderModifier) error
```

`Side` is `portfolio.Buy` or `portfolio.Sell`. `qty` is a share count. With no modifiers the order is a market order with `DayOrder` time-in-force.

```go
batch.Order(ctx, spy, portfolio.Buy, 100)
batch.Order(ctx, tlt, portfolio.Sell, 50, portfolio.Limit(95.00))
batch.Order(ctx, spy, portfolio.Sell, 100, portfolio.Stop(380.00))
```

### Order modifiers

Modifiers are variadic and combine freely.

| Modifier | Category | Behavior |
|----------|----------|----------|
| `Limit(price)` | Order type | Maximum buy price or minimum sell price. |
| `Stop(price)` | Order type | Trigger market order at threshold (stop loss). |
| `Limit(...) + Stop(...)` | Order type | Combined: stop-limit. Activates at the stop price, fills only at the limit price or better. |
| `DayOrder` | Time in force | Cancel at market close if not filled. **Default.** |
| `GoodTilCancel` | Time in force | Stay open until filled or cancelled (typically up to 180 days). |
| `GoodTilDate(t)` | Time in force | Stay open until a specific date. |
| `FillOrKill` | Time in force | Fill entirely or cancel immediately. |
| `ImmediateOrCancel` | Time in force | Fill what's possible immediately, cancel the rest. |
| `OnTheOpen` | Time in force | Fill only at the opening auction. |
| `OnTheClose` | Time in force | Fill only at the closing auction. |
| `WithJustification(s)` | Annotation | Attach an explanation to the resulting transaction. |
| `WithLotSelection(method)` | Tax | Override the portfolio default lot selection (FIFO/LIFO/HIFO/LOFO) for this order. |
| `WithBracket(stopLoss, takeProfit)` | Group | Attach an OCO exit pair that activates after the entry fills. **Batch-only.** |
| `OCO(legA, legB)` | Group | Two linked exit orders; filling one cancels the other. **Batch-only.** |

Order type, time in force, justification, and group modifiers can all combine on the same call:

```go
batch.Order(ctx, spy, portfolio.Buy, 100,
    portfolio.Limit(150.00),
    portfolio.GoodTilCancel,
    portfolio.WithJustification("breakout above 150"),
)
```

### Bracket exits

`WithBracket` attaches a stop-loss and take-profit pair that activates as an OCO when the entry fills. Exit targets accept either an absolute price or a percentage offset from the fill price:

| Constructor | Description |
|-------------|-------------|
| `StopLossPrice(price)` | Stop loss at a fixed price. |
| `StopLossPercent(pct)` | Stop loss `pct` percent below the fill price (e.g. 5 for 5%). |
| `TakeProfitPrice(price)` | Take profit at a fixed price. |
| `TakeProfitPercent(pct)` | Take profit `pct` percent above the fill price. |

```go
// Buy SPY with -5% stop and +10% take profit relative to fill price.
batch.Order(ctx, spy, portfolio.Buy, 100,
    portfolio.Limit(400),
    portfolio.WithBracket(
        portfolio.StopLossPercent(5),
        portfolio.TakeProfitPercent(10),
    ),
)

// Same shape using absolute prices.
batch.Order(ctx, spy, portfolio.Buy, 100,
    portfolio.Limit(400),
    portfolio.WithBracket(
        portfolio.StopLossPrice(380),
        portfolio.TakeProfitPrice(440),
    ),
)
```

### OCO exits

`OCO` creates two linked orders from a single `batch.Order` call. When one leg fills the other is cancelled. Use `StopLeg(price)` for a stop-order leg and `LimitLeg(price)` for a limit-order leg.

```go
// Protect an existing long: stop out at 380 or take profit at 440.
batch.Order(ctx, spy, portfolio.Sell, 100,
    portfolio.OCO(portfolio.StopLeg(380), portfolio.LimitLeg(440)),
)
```

In backtesting the simulated broker evaluates bracket and OCO exits against each bar's high and low. If both legs could trigger on the same bar the stop loss wins (pessimistic assumption). Bracket and OCO modifiers are batch-only -- they cannot be passed through `Account.Order` directly.

## Short selling

A short position is one with negative quantity. Shorts open when a sell order executes against zero (or already-short) holdings, and close (cover) when a buy order brings the quantity back toward zero.

**Imperative shorting:**

```go
// Open a short of 100 XYZ.
batch.Order(ctx, xyz, portfolio.Sell, 100)

// Cover the full short.
batch.Order(ctx, xyz, portfolio.Buy, 100)
```

A buy that exceeds the short quantity covers the short flat and creates a fresh long for the excess.

**Declarative shorting:** use a negative weight in an `Allocation`. This is the same convention as Zipline and QuantConnect.

```go
batch.RebalanceTo(ctx, portfolio.Allocation{
    Date: eng.CurrentDate(),
    Members: map[asset.Asset]float64{
        spy: 1.20,  // 120% long SPY
        qqq: -0.20, // 20% short QQQ
    },
})
```

`RebalanceTo` covers any short positions that fall off the target list, the same way it sells longs that fall off. Long and short weights mix freely. The sum of absolute weights may exceed 1.0 for leveraged or market-neutral books; the result must still satisfy the configured initial-margin requirement or the engine will reject orders that increase exposure.

`Position(ast)` and `PositionValue(ast)` return negative values for shorts. Borrow fees and dividend obligations on shorts are debited automatically by the engine via `PortfolioManager` -- strategy code does not need to handle them.

## Margin

The portfolio tracks margin state through three numbers:

```go
ratio      := port.MarginRatio()      // equity / short market value; NaN when no shorts
deficiency := port.MarginDeficiency() // dollars below maintenance margin; 0 if compliant
buyPower   := port.BuyingPower()      // cash minus initial margin reserved for existing shorts
```

- **`MarginRatio`** is equity divided by short market value. It is NaN when there are no short positions; check for `math.IsNaN(ratio)` before comparing.
- **`MarginDeficiency`** is the dollar gap between current equity and the maintenance margin requirement on existing shorts. Zero means the account is compliant. A positive value means the account is breached.
- **`BuyingPower`** is cash minus the initial margin reserved for the existing short book. Use it as the upper bound on new long buying capacity.

A margin call triggers as soon as `MarginDeficiency() > 0`. By default the engine calls the strategy's `MarginCallHandler` if one is implemented; if no handler is registered, or if the handler does not fully clear the deficiency, the engine **automatically liquidates short positions proportionally** until the deficiency reaches zero. Auto-liquidation runs on a `SkipMiddleware` batch so risk middleware cannot block the emergency cover.

Margin parameters are set when constructing the account:

```go
acct := portfolio.New(
    portfolio.WithCash(100_000, startDate),
    portfolio.WithInitialMargin(0.50),     // Reg T default: 50%
    portfolio.WithMaintenanceMargin(0.30), // pvbt default: 30%
)
```

## MarginCallHandler

`engine.MarginCallHandler` is an optional interface a strategy can implement to take over margin-call response. The engine looks for it whenever a deficiency is detected, before falling back to auto-liquidation.

```go
package engine

type MarginCallHandler interface {
    OnMarginCall(ctx context.Context, eng *Engine, port portfolio.Portfolio, batch *portfolio.Batch) error
}
```

Order of operations:

1. Engine detects `MarginDeficiency() > 0`.
2. If the strategy implements `MarginCallHandler`, the engine creates a fresh batch with `SkipMiddleware = true`, calls `OnMarginCall(ctx, eng, port, batch)`, and executes the resulting orders.
3. If `MarginDeficiency() == 0` after the handler runs, recovery is complete.
4. If a deficiency remains, the engine runs proportional auto-liquidation (also on a `SkipMiddleware` batch) until the deficiency clears.

Example -- cover every short immediately when called:

```go
func (s *PairsStrategy) OnMarginCall(ctx context.Context, _ *engine.Engine, port portfolio.Portfolio, batch *portfolio.Batch) error {
    zerolog.Ctx(ctx).Warn().Msg("margin call: covering all shorts")

    for held, qty := range port.Holdings() {
        if qty < 0 {
            // -qty is a positive share count for the buy-to-cover.
            if err := batch.Order(ctx, held, portfolio.Buy, -qty); err != nil {
                return fmt.Errorf("cover %s: %w", held.Ticker, err)
            }
        }
    }

    return nil
}
```

Because the margin-call batch sets `SkipMiddleware = true`, position-size limits, exposure caps, and other risk middleware do **not** run on the orders the handler queues. This is intentional -- emergency covers must never be blocked by risk rules.

## Why Portfolio is read-only in Compute

Three reasons:

1. **Middleware integrity.** Risk profiles, position-size caps, drawdown circuit breakers, and tax-aware substitution all run on the assembled batch *after* `Compute` returns. Writing directly to the account would bypass them and leave the live middleware chain inconsistent with the strategy's view of the world.
2. **Determinism.** The engine drains a single batch per frame, in a specific order: middleware, broker submission, fill draining, equity update. Direct mutations from inside `Compute` would interleave with that sequence and break replay.
3. **Live-vs-backtest parity.** In live mode the broker confirms fills asynchronously. The batch model is the same in both modes -- strategy code writes intent, the engine handles execution -- so a strategy that compiles for backtest also runs against a real broker without changes.

The `Portfolio` interface in `portfolio/portfolio.go` deliberately exposes only query methods. The mutation surface (`Record`, `UpdatePrices`, `ExecuteBatch`, etc.) lives on the unexported `PortfolioManager` interface, which strategy code never sees.

## See also

- [signals-and-weighting.md](./signals-and-weighting.md) -- selectors, weighting functions, and signal construction
- [common-pitfalls.md](./common-pitfalls.md) -- mistakes that bite strategy authors, including order/portfolio gotchas
