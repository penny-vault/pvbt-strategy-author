_Last verified against pvbt 0.6.0._

# Scheduling and Warmup

## tradecron expressions

A tradecron expression is a standard 5-field cron string (`minute hour day-of-month month day-of-week`) extended with market-aware directives. The schedule controls when the engine calls `Compute`.

```go
func (s *MyStrategy) Describe() engine.StrategyDescription {
    return engine.StrategyDescription{
        Schedule: "@close @monthend",
    }
}
```

The schedule field is required. The engine returns an error if it is empty.

Supported directives:

- `@open` -- market open; replaces the minute and hour fields.
- `@close` -- market close; replaces the minute and hour fields.
- `@daily` -- shorthand for `@open * * *` (every trading day at the open).
- `@weekbegin` -- first trading day of the week; replaces day-of-month.
- `@weekend` -- last trading day of the week; replaces day-of-month.
- `@monthbegin` -- first trading day of the month; replaces day-of-month. Defaults to `@open` time.
- `@monthend` -- last trading day of the month; replaces day-of-month. Defaults to `@close` time.
- `@quarterbegin` -- first trading day of the quarter; replaces day-of-month. Defaults to `@open` time.
- `@quarterend` -- last trading day of the quarter; replaces day-of-month. Defaults to `@close` time.

Directives compose with each other and with standard cron fields. The package always pairs the schedule with `tradecron.RegularHours`, which advances any tick that would land on a weekend or holiday to the next valid trading day.

Common schedules:

| Expression | When `Compute` runs |
|------------|---------------------|
| `@daily` | Every trading day at market open |
| `@open * * *` | Every trading day at market open |
| `@close * * *` | Every trading day at market close |
| `@monthend` | Last trading day of each month at market close |
| `@close @monthend` | Last trading day of each month at market close (explicit) |
| `@monthbegin` | First trading day of each month at market open |
| `@weekend` | Last trading day of each week at market close |
| `@weekbegin` | First trading day of each week at market open |
| `@quarterend` | Last trading day of each quarter at market close |
| `@quarterbegin` | First trading day of each quarter at market open |
| `@open @monthend` | Last trading day of each month at market open |
| `0 10 * * *` | Every trading day at 10:00 AM Eastern |
| `15 @open * * *` | 15 minutes after market open every trading day |
| `*/5 * * * *` | Every 5 minutes during regular trading hours |

## Time zone

Market-aware directives (`@open`, `@close`, `@monthend`, `@weekbegin`, etc.) always produce timestamps in `America/New_York`. Plain numeric cron fields (`0 10 * * *`) are also interpreted as Eastern in practice because `RegularHours` is anchored to the US equity session.

Data providers must return data in matching Eastern timestamps. If the schedule produces Eastern ticks but the data provider emits UTC, the engine's time-range filtering returns empty DataFrames silently. This is one of the most common sources of mysterious "no data" failures -- always confirm that custom data providers normalize to Eastern.

## Warmup

Warmup is declared in `Describe()` as an integer number of trading days:

```go
func (s *MyStrategy) Describe() engine.StrategyDescription {
    return engine.StrategyDescription{
        Schedule: "@monthend",
        Warmup:   252, // one trading year
    }
}
```

Semantics:

- `Warmup` is measured in **trading days**, not calendar days. `252` is roughly one year, `126` is six months, `63` is one quarter, `21` is one month.
- Before the first `Compute` call, the engine resolves the first scheduled trade date, walks back `Warmup` trading days, and calls the data layer for `MetricClose` over that window.
- The engine validates every asset reachable through reflection: `asset.Asset` fields, static `universe.Universe` fields, the benchmark, and any child strategies' assets. Index universes are not validated this way (their membership is dynamic).
- A negative `Warmup` is a fatal configuration error.
- If a strategy has children (a meta-strategy), the engine takes the maximum `Warmup` across the parent and all children.

Set `Warmup` to the longest lookback your `Compute` will request. A momentum strategy that calls `signal.Momentum(ctx, universe, portfolio.Months(12))` needs `Warmup: 252` at minimum.

## Strict vs. permissive mode

The engine has two modes for handling assets that lack enough warmup history. The mode is set with `engine.WithDateRangeMode(...)`:

```go
const (
    DateRangeModeStrict     DateRangeMode = iota // default
    DateRangeModePermissive
)
```

**Strict (default).** The engine fails fast. If any asset has fewer than `Warmup` non-NaN closes in the warmup window, the run aborts with an error listing each short asset and how many trading days it is missing:

```
engine: insufficient warmup data: TSLA (short by 47 days), META (short by 12 days)
```

**Permissive.** The engine walks the start date forward one trading day at a time until every asset has enough history, then begins the run from that adjusted start. If no valid start exists before the requested end date, the run still errors out.

Selecting permissive mode:

```go
eng := engine.New(strategy,
    engine.WithDataProvider(provider),
    engine.WithAssetProvider(provider),
    engine.WithDateRangeMode(engine.DateRangeModePermissive),
)
```

When using the CLI, strict mode is the default and there is no flag to switch -- permissive mode is opted into by custom entry points (tests, custom binaries) only. Strategies that include young tickers should either declare a later start date or run from a custom entry point in permissive mode.

## When Compute is called

`Compute` is called at the scheduled tick time, in Eastern time, after the engine has fetched data for currently held assets and after any child strategies for the same tick have been processed.

Concrete cases:

- `@daily` / `@open * * *` -- 9:30 AM Eastern on every trading day.
- `@close * * *` -- 4:00 PM Eastern on every trading day.
- `@monthend` -- 4:00 PM Eastern on the last trading day of the month (e.g. 2026-01-30, not 2026-01-31, because January 31 is a Saturday).
- `@monthbegin` -- 9:30 AM Eastern on the first trading day of the month.
- `0 10 * * *` -- 10:00 AM Eastern on every trading day.

If the nominal date is a holiday, `RegularHours` advances the tick to the next valid trading day rather than skipping it. So `@monthend` for a month whose last calendar day is a market holiday fires on the prior trading day.

In a meta-strategy, the engine always runs all children for a given tick before running the parent's `Compute`, so the parent observes the children's post-trade state.

## Common pitfalls

- **Using `@daily` when you mean `@monthend`.** `@daily` fires every trading day. A strategy that only rebalances monthly but is scheduled `@daily` will pay transaction costs daily. Use `@monthend` (or `@monthbegin`, `@weekend`, etc.) for periodic rebalancing.
- **Using `@open @monthend` when you want closing prices.** `@monthend` defaults to market close. If you override the time flag to `@open`, your `Compute` runs at the morning of the last trading day, which means signals are computed from the prior day's close, not the same-day close. Match the time flag to the data your signal uses.
- **Underestimating warmup.** Setting `Warmup: 21` and then asking for `signal.Momentum(ctx, u, portfolio.Months(12))` will silently produce NaN signals (in permissive mode the start date drifts further than expected; in strict mode the run fails up front). Always set `Warmup` to the longest lookback in `Compute`.
- **Forgetting child warmup.** A meta-strategy's `Warmup` does not need to include the children's warmup -- the engine takes the max automatically -- but the parent must still declare its own warmup if it computes its own signals.
- **Mismatched time zones in custom data providers.** A custom provider that emits UTC timestamps will silently return empty DataFrames when the schedule fires at Eastern times. Normalize data to Eastern before returning it.
- **Assuming `@monthend` means the last calendar day.** It means the last *trading* day of the month. Holidays and weekends shift it earlier, never later.
- **Relying on permissive mode in production.** Permissive mode hides data-quality problems by silently shifting the start date. Prefer strict mode for backtests so missing data is loud.

## See also

- [./strategy-api.md](./strategy-api.md) -- the `Describe()` interface and `StrategyDescription` struct that hold `Schedule` and `Warmup`.
- [./common-pitfalls.md](./common-pitfalls.md) -- broader catalog of mistakes when writing pvbt strategies.
