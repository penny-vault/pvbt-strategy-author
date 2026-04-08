_Last verified against pvbt 0.6.0._

# Common Pitfalls

This reference catalogs the recurring bugs and anti-patterns that show up in pvbt strategies. It is the document the strategy-reviewer agent leans on most heavily during a "quant red flags" pass. Each entry follows the same shape: definition, what the bad code looks like, what the good code looks like, and how a reviewer should detect it. Pitfalls are listed roughly in order of how often they cause silent backtest lies versus loud failures -- the silent ones come first because they are harder to catch.

## Survivorship bias

Survivorship bias is the use of an asset list that only contains tickers that survived to the present, omitting names that were delisted, merged, or went bankrupt during the backtest window. Performance is overstated because the strategy can never accidentally hold a future loser.

**Symptom:** A static universe of "the current S&P 500" or "today's Nasdaq 100", typically built from a hand-picked ticker list or a CSV pulled from the index's current composition.

```go
// BAD: hard-coded list of today's S&P 500 members.
type Strategy struct {
    Stocks universe.Universe `pvbt:"stocks" default:"AAPL,MSFT,NVDA,AMZN,GOOGL,META,..."`
}

// BAD: built in Setup from a fixed list, regardless of historical membership.
func (s *Strategy) Setup(eng *engine.Engine) {
    s.stocks = eng.Universe(eng.Asset("AAPL"), eng.Asset("MSFT"), eng.Asset("NVDA"))
}
```

**Fix:** Use an index universe that resolves membership at every simulation date. The pvbt engine ships index providers for `SPX`, `NDX`, and `us-tradable`. `Assets(t)` returns the historical members as of `t`, so a 2005 backtest sees the 2005 S&P 500 members, not the 2026 members.

```go
// GOOD: index universe with point-in-time membership.
func (s *Strategy) Setup(eng *engine.Engine) {
    s.stocks = eng.IndexUniverse("SPX")
}

// GOOD: us-tradable for broad US equity strategies.
func (s *Strategy) Setup(eng *engine.Engine) {
    s.stocks = eng.IndexUniverse("us-tradable")
}
```

Static universes (`eng.Universe(...)`, `default:"SPY,EFA,EEM"`) are appropriate only for fixed asset allocations across well-known ETFs that have existed for the entire backtest window. Anything that aspires to "all stocks meeting condition X" must use an index universe.

**How to detect in review:** Grep for `eng.Universe(` and `universe.Universe` struct fields with hard-coded ticker lists. Ask: would this list have been the same 10 years ago? If the strategy's premise is "broad equity selection" but the universe is static, it is survivorship-biased. Also look for any ticker list mined from an external source ("S&P 500 constituents", "Russell 2000 members") that does not go through `eng.IndexUniverse(...)`.

## Lookahead bias

Lookahead bias is the use of information at simulation time `t` that would not have been available until after `t`. The classic forms are reading future prices, future fundamentals, or future index membership; the subtle forms are off-by-one errors in lookback windows and signal computations that include the current bar before it has closed.

**Symptom:** Pulling data with an explicit end date later than `eng.CurrentDate()`, indexing into a DataFrame past its last row, using `data.MetricClose` at `@open` (the close has not happened yet), or computing a signal that depends on `df.Last()` when the strategy is scheduled to fire before the current bar completes.

```go
// BAD: fetching past the current simulation date.
df, err := s.Stocks.Window(ctx, portfolio.Days(0), data.MetricClose)
endNextWeek := eng.CurrentDate().AddDate(0, 0, 7)
df2, err := eng.Fetch(ctx, eng.CurrentDate(), endNextWeek, data.MetricClose, s.stocks.Assets(eng.CurrentDate())...)

// BAD: using same-day close at @open (close has not occurred yet).
func (s *Strategy) Describe() engine.StrategyDescription {
    return engine.StrategyDescription{Schedule: "@open @monthend"}
}
func (s *Strategy) Compute(ctx context.Context, eng *engine.Engine, port portfolio.Portfolio, batch *portfolio.Batch) error {
    df, _ := s.Stocks.Window(ctx, portfolio.Days(1), data.MetricClose)
    todayClose := df.Last() // wrong: today's close is not yet known at the open
    // ...
}
```

**Fix:** Always end the data window at `eng.CurrentDate()` (which is what `Window` and `At` already do by default). Match the metric to the schedule: `@open` strategies should use the previous trading day's close, `@close` strategies may use today's close. Fundamentals from the metrics table are point-in-time (filed-as-of), so they are safe at any simulation date, but raw earnings releases can leak future information if pulled directly without the as-of filter.

```go
// GOOD: rely on Window/At to bound at current simulation date.
df, err := s.Stocks.Window(ctx, portfolio.Months(6), data.MetricClose)

// GOOD: @close schedule using same-day close.
func (s *Strategy) Describe() engine.StrategyDescription {
    return engine.StrategyDescription{Schedule: "@close @monthend"}
}
```

**How to detect in review:** Look for any `eng.Fetch(...)` call with an explicit end date and verify it is `<=` `eng.CurrentDate()`. Cross-check the schedule's time-of-day directive (`@open` vs `@close`) against the metrics the strategy reads. Look for index access like `df.ValueAt(asset, metric, futureDate)` or `df.At(eng.CurrentDate().AddDate(...))`. Any data access whose date is computed by adding a positive offset to `CurrentDate()` is a smell.

## Leaked state between Compute calls

`Compute` is called once per scheduled tick and the same strategy struct receiver persists across calls. Any field mutated by a previous `Compute` survives into the next, which is sometimes intentional (caching warmup values, tracking trade history) but is more often a bug -- accumulators that should reset, sticky position flags that never clear, or maps that grow without bound.

**Symptom:** Strategy struct has unexported fields written from inside `Compute` that are not idempotent. Common patterns: a `lastSignal` cached but never invalidated, a counter incremented every call, a map keyed by date that is never trimmed, or a "first time?" boolean that controls behavior on the first call but then leaks into normal operation.

```go
// BAD: stale cached signal from a previous tick.
type Strategy struct {
    Lookback int `pvbt:"lookback" default:"6"`

    cached map[asset.Asset]float64 // never cleared
}

func (s *Strategy) Compute(ctx context.Context, eng *engine.Engine, port portfolio.Portfolio, batch *portfolio.Batch) error {
    if len(s.cached) == 0 {
        s.cached = computeSignals(...) // only computed once, ever
    }
    // ... uses s.cached forever, even months later
}

// BAD: position flag that locks the strategy in one mode permanently.
type Strategy struct {
    inRiskOff bool
}

func (s *Strategy) Compute(ctx context.Context, eng *engine.Engine, port portfolio.Portfolio, batch *portfolio.Batch) error {
    if !s.inRiskOff && riskScore < threshold {
        s.inRiskOff = true
    }
    // never sets s.inRiskOff back to false
}
```

**Fix:** Treat `Compute` as a pure function from `(ctx, eng, port)` -> `batch` writes whenever possible. If you must cache something across calls, key it on a value that changes (the simulation date, a hash of the universe membership, etc.) so the cache invalidates on its own. If you must track state, document the lifecycle clearly and make sure there is a code path that clears it. The portfolio holds the truth about current positions -- query `port.Holdings()` and `port.Position(asset)` instead of mirroring them on the struct.

```go
// GOOD: stateless. Recomputes from current data every tick.
func (s *Strategy) Compute(ctx context.Context, eng *engine.Engine, port portfolio.Portfolio, batch *portfolio.Batch) error {
    df, err := s.Stocks.Window(ctx, portfolio.Months(s.Lookback), data.MetricClose)
    // ...
}

// GOOD: cache keyed on the simulation date, naturally invalidates.
type Strategy struct {
    cacheDate time.Time
    cache     map[asset.Asset]float64
}

func (s *Strategy) Compute(ctx context.Context, eng *engine.Engine, port portfolio.Portfolio, batch *portfolio.Batch) error {
    if !s.cacheDate.Equal(eng.CurrentDate()) {
        s.cache = recompute(...)
        s.cacheDate = eng.CurrentDate()
    }
    // ...
}
```

**How to detect in review:** Grep for unexported fields on the strategy struct that are not parameters. Trace every write site. Any field that is written from inside `Compute` and read on a later `Compute` call is a state leak unless the lifecycle is explicit and tested. Maps and slices that grow but never shrink are a particularly common form. Position flags (`inRiskOff bool`, `holdingX bool`) duplicating data already in `port.Holdings()` are always a bug -- delete them and query the portfolio.

## Insufficient warmup

Warmup is the number of trading days of historical data the engine pre-fetches before the first `Compute` call. If a strategy asks `Window` for a longer lookback than its declared warmup, it will receive a short DataFrame on the first few ticks. In strict mode (the default) the engine refuses to start when an asset is short. In permissive mode the start date silently drifts, which hides the problem and shortens the effective backtest.

**Symptom:** `Warmup` declared as a small number (or omitted entirely) but `Compute` calls `Window` with a multi-month or multi-year lookback. Or `Warmup` is set to one value while a parameter (`Lookback int`) controls a longer one.

```go
// BAD: warmup is way too short for the lookback.
func (s *Strategy) Describe() engine.StrategyDescription {
    return engine.StrategyDescription{
        Schedule: "@monthend",
        Warmup:   21, // ~1 month
    }
}

func (s *Strategy) Compute(ctx context.Context, eng *engine.Engine, port portfolio.Portfolio, batch *portfolio.Batch) error {
    df, err := s.Assets.Window(ctx, portfolio.Months(12), data.MetricClose) // needs ~252 days
    // ...
}

// BAD: parameter-driven lookback ignored by Warmup.
type Strategy struct {
    Lookback int `pvbt:"lookback" default:"24"` // user can crank to 60
}

func (s *Strategy) Describe() engine.StrategyDescription {
    return engine.StrategyDescription{Warmup: 252} // covers default but not max
}
```

**Fix:** Set `Warmup` to the longest lookback the strategy will ever request, measured in trading days (not calendar days). Roughly: 21 = 1 month, 63 = 1 quarter, 126 = 6 months, 252 = 1 year. If the lookback is parameter-driven, set `Warmup` to the maximum the parameter could plausibly take, not its default.

```go
// GOOD: warmup matches the longest lookback Compute will request.
func (s *Strategy) Describe() engine.StrategyDescription {
    return engine.StrategyDescription{
        Schedule: "@monthend",
        Warmup:   252, // 1 trading year, matches Months(12) lookback
    }
}
```

**How to detect in review:** For every `Window(ctx, period, ...)` call in `Compute`, compute the period in trading days and confirm `Describe().Warmup` is at least as large. For parameter-driven lookbacks, check the maximum value the parameter could take (look at `default`, `suggest` presets, and any documented range) and use that. Also verify `Warmup` is non-negative -- a negative value is a fatal configuration error.

## Time zone mismatches

The pvbt engine operates entirely in `America/New_York`. Tradecron's market-aware directives (`@open`, `@close`, `@monthend`, `@weekbegin`, etc.) all produce Eastern timestamps. Numeric cron fields (`0 10 * * *`) are also interpreted as Eastern in practice because `RegularHours` is anchored to the US equity session. Data providers must return timestamps in the same zone, or the engine's range filtering silently returns empty DataFrames.

**Symptom:** Custom data providers that emit UTC timestamps. Strategies that compare `eng.CurrentDate()` to `time.Now().UTC()`. Tests that build dates with `time.Date(..., time.UTC)` and pass them to the engine without converting. "No data" failures that disappear when the strategy is scheduled at midnight UTC.

```go
// BAD: custom provider returning UTC timestamps.
func (p *MyProvider) Fetch(ctx context.Context, start, end time.Time, ...) (*data.DataFrame, error) {
    rows := db.Query(...)
    times := make([]time.Time, 0)
    for _, row := range rows {
        times = append(times, row.Date.UTC()) // wrong zone
    }
    // engine schedules ticks at 16:00 ET; this DataFrame's times are at 00:00 UTC,
    // so engine.Fetch returns an empty result and Compute sees df.Len() == 0
}

// BAD: comparing eng.CurrentDate() to wall-clock UTC.
if eng.CurrentDate().UTC().Equal(time.Now().UTC()) { // never matches in backtest
    // ...
}
```

**Fix:** Custom data providers must convert all timestamps to `America/New_York` before returning. Date-only data (one row per trading day) should be anchored at 16:00 ET (market close) so it lines up with `@close` directives. Tests should construct dates in Eastern, not UTC. Inside `Compute`, treat `eng.CurrentDate()` as the canonical time -- never compare it to wall-clock time.

```go
// GOOD: provider normalizes to Eastern.
var eastern = func() *time.Location {
    loc, _ := time.LoadLocation("America/New_York")
    return loc
}()

func (p *MyProvider) Fetch(ctx context.Context, start, end time.Time, ...) (*data.DataFrame, error) {
    // anchor date-only rows at 16:00 ET to match @close directives
    times = append(times, time.Date(row.Date.Year(), row.Date.Month(), row.Date.Day(), 16, 0, 0, 0, eastern))
}
```

**How to detect in review:** Grep for `time.UTC` and `time.Now()` in strategy and provider code. Any timestamp passed to or from the engine that is constructed in UTC is suspect. Look for "no data" debug logs paired with non-zero scheduled ticks -- that pattern is almost always a time zone mismatch. In tests, confirm `startDate` and `endDate` are constructed with `time.LoadLocation("America/New_York")` or via the snapshot provider (which already normalizes). For custom providers, read the implementation: any `row.Date.UTC()` or `time.Date(..., time.UTC)` for market data is a bug.

## Over-parameterization

A strategy is over-parameterized when it exposes more tuning knobs than its underlying idea justifies. Every exported struct field becomes a CLI flag, and every flag is an opportunity to overfit -- to pick the value that looked best in backtest, not the value that captures a real signal. The relationship is multiplicative: 8 parameters with 5 plausible values each is a search space of 390,625 backtests, and the best of those is almost certainly noise.

**Symptom:** Strategy struct with 10+ exported fields, many describing implementation details (smoothing constants, epsilon guards, internal sample sizes, magic offsets) rather than user-facing decisions. Defaults that have no justification beyond "this is what won the grid search". Parameters added mid-development to handle a tradeoff the author encountered in one specific year.

```go
// BAD: every internal knob is exposed.
type Strategy struct {
    Lookback         int     `pvbt:"lookback"          default:"6"`
    LookbackShort    int     `pvbt:"lookback-short"    default:"3"`
    Smoothing        float64 `pvbt:"smoothing"         default:"0.27"`  // unjustified
    Epsilon          float64 `pvbt:"epsilon"           default:"1e-8"`  // implementation detail
    BufferSize       int     `pvbt:"buffer-size"       default:"42"`   // why 42?
    SignalThreshold  float64 `pvbt:"signal-threshold"  default:"0.0173"` // grid-searched
    DecayRate        float64 `pvbt:"decay-rate"        default:"0.94"`
    VolWindow        int     `pvbt:"vol-window"        default:"19"`
    VolCap           float64 `pvbt:"vol-cap"           default:"0.32"`
    MaxPositions     int     `pvbt:"max-positions"     default:"3"`
    RebalanceBand    float64 `pvbt:"rebalance-band"    default:"0.04"`
    RiskOnFloor      float64 `pvbt:"risk-on-floor"     default:"0.18"`
}
```

**Fix:** Aim for the smallest set of parameters that captures the essential decisions of the strategy. Three to five parameters is enough for most ideas; more than ten is a red flag. Expose the values a thoughtful user would plausibly want to vary (lookback windows, universes, position counts, documented mode switches). Hard-code everything else as Go constants in the strategy file. Use `suggest` presets to capture variants instead of letting users grid-search raw flags.

```go
// GOOD: a small, defensible parameter surface.
type Strategy struct {
    RiskOn   universe.Universe `pvbt:"risk-on"  desc:"Assets to rotate between" default:"SPY,EFA,EEM"`
    RiskOff  universe.Universe `pvbt:"risk-off" desc:"Safe-haven asset"         default:"SHY"`
    Lookback int               `pvbt:"lookback" desc:"Momentum lookback months"  default:"6" suggest:"Fast=3|Classic=6|Slow=12"`
}

const (
    rebalanceBand   = 0.05  // hard-coded, no need to expose
    signalEpsilon   = 1e-9  // implementation detail
)
```

**How to detect in review:** Count exported fields on the strategy struct. More than ten is a red flag; more than fifteen almost guarantees overfitting. For each parameter, ask: can the author write a one-sentence explanation of what changing it should do, justified by theory or by the original paper? If the answer is "it's the value that worked best", the parameter is overfit by construction. Look for unusually-precise default values (`0.0173`, `42`, `19`) -- those are usually grid-search artifacts. Also look for parameters that share a unit and could be collapsed (a `lookback` and `lookback-short` and `lookback-long` triple smells like one parameter pretending to be three).

## Silent failures

A silent failure is any code path that produces an apparently-valid result while actually losing information. The pvbt convention is to surface errors loudly: `fmt.Errorf("context: %w", err)` to wrap with context, log at error level, and either return the error from `Compute` (to halt the backtest) or return `nil` (to skip this tick) -- but never both swallow the error and produce a fabricated value.

**Symptom:** Empty `if err != nil { return nil }` that drops the error without logging. Code that catches an error and substitutes a hard-coded fallback ("if data fetch fails, use the previous allocation"). Use of `_` to discard errors. `//nolint` directives suppressing errcheck. NaN-tolerating math that hides upstream NaN bugs (`if math.IsNaN(x) { x = 0 }`). Empty universes treated as "no positions today" instead of as a bug.

```go
// BAD: error swallowed, no log, no surface.
df, err := s.Assets.Window(ctx, portfolio.Months(6), data.MetricClose)
if err != nil {
    return nil
}

// BAD: hard-coded fallback hides the failure.
df, err := s.Assets.Window(ctx, portfolio.Months(6), data.MetricClose)
if err != nil {
    return batch.RebalanceTo(ctx, s.lastGoodPlan...) // pretends nothing happened
}

// BAD: discarding errors entirely.
df, _ := s.Assets.Window(ctx, portfolio.Months(6), data.MetricClose)
score := df.Pct(df.Len() - 1).Last() // panics on the next tick if df is nil

// BAD: silently degrading on NaN inputs.
mom := momentum.Value(spy, data.MetricClose)
if math.IsNaN(mom) {
    mom = 0 // pretends the asset has zero momentum instead of investigating why it's NaN
}
```

**Fix:** Wrap errors with context using `fmt.Errorf("context: %w", err)`, log them via `zerolog.Ctx(ctx)` at error level, and return the error from `Compute` if it is unrecoverable or `nil` (with the log) if skipping this tick is acceptable. Never both swallow and continue with stale data. Fail loudly on NaN -- a NaN signal usually means missing data or a divide-by-zero, both of which are bugs the author wants to find. Empty universes should be checked explicitly and treated as a configuration error, not an empty allocation.

```go
// GOOD: log and skip this tick.
df, err := s.Assets.Window(ctx, portfolio.Months(6), data.MetricClose)
if err != nil {
    zerolog.Ctx(ctx).Error().Err(err).Msg("window fetch failed")
    return nil
}

// GOOD: surface as a fatal error.
df, err := s.Assets.Window(ctx, portfolio.Months(6), data.MetricClose)
if err != nil {
    return fmt.Errorf("window fetch for %s: %w", eng.CurrentDate().Format("2006-01-02"), err)
}

// GOOD: validate empty universe explicitly.
members := s.stocks.Assets(eng.CurrentDate())
if len(members) == 0 {
    return fmt.Errorf("universe is empty at %s -- check data provider coverage",
        eng.CurrentDate().Format("2006-01-02"))
}
```

**How to detect in review:** Grep for `if err != nil { return nil }` (no logging, no wrap). Grep for `, _ :=` and `, _ =` discarding errors. Search for `//nolint` (the codebase forbids these -- any occurrence is a violation of CLAUDE.md). Look for `math.IsNaN(...)` checks paired with assignment of `0` or another sentinel rather than an error log. Look for cached "last good" values that are used as fallbacks. Any place a strategy continues after an error without logging it is a silent failure.

## Logging mistakes

The pvbt convention is to log via `zerolog.Ctx(ctx)`, never via `fmt.Println`, the standard `log` package, or a globally-stored logger. The context-bound logger picks up the request-scoped fields the engine attaches (`strategy`, `date`, `tick`) and routes through whatever sink the harness configured. Using anything else loses the context fields and bypasses the log-level filter.

**Symptom:** `fmt.Println` or `fmt.Printf` debugging that was never removed. Calls to the standard `log` package (`log.Printf`, `log.Println`). Strategies storing a `*zerolog.Logger` on the struct and using it instead of fetching from context. Logging at the wrong level -- info for routine ticks (which spams the log) or debug for genuine failures (which hides them).

```go
// BAD: fmt.Println instead of zerolog.
fmt.Println("computing signal for", eng.CurrentDate())

// BAD: standard log package.
log.Printf("rebalancing to %v", plan)

// BAD: stored logger that loses ctx fields.
type Strategy struct {
    log zerolog.Logger
}

func (s *Strategy) Setup(eng *engine.Engine) {
    s.log = log.With().Str("strategy", "myname").Logger()
}

func (s *Strategy) Compute(ctx context.Context, ...) error {
    s.log.Info().Msg("computing") // misses ctx-bound fields like date
}

// BAD: info-level spam at every tick.
log := zerolog.Ctx(ctx)
log.Info().Float64("score", score).Msg("computed score") // fires hundreds of times
```

**Fix:** Always fetch the logger via `zerolog.Ctx(ctx)` at the top of `Compute` and use it directly. Use levels deliberately: `Debug` for routine per-tick traces, `Info` for once-per-run lifecycle events, `Warn` for recoverable problems, `Error` for failures the strategy is going to skip past, and `Fatal` never (let the engine decide whether to halt). Attach structured fields with `.Str(...).Float64(...).Int(...)` rather than formatting them into the message string -- structured fields are searchable and the engine's log harness preserves them.

```go
// GOOD: ctx-scoped logger, structured fields, appropriate level.
func (s *Strategy) Compute(ctx context.Context, eng *engine.Engine, port portfolio.Portfolio, batch *portfolio.Batch) error {
    log := zerolog.Ctx(ctx)

    df, err := s.Assets.Window(ctx, portfolio.Months(s.Lookback), data.MetricClose)
    if err != nil {
        log.Error().Err(err).Msg("window fetch failed")
        return nil
    }

    log.Debug().
        Int("members", len(df.AssetList())).
        Int("days", df.Len()).
        Msg("fetched window")

    // ...
}
```

**How to detect in review:** Grep for `fmt.Println`, `fmt.Printf`, `log.Printf`, `log.Println` in strategy files -- all of those are wrong. Grep for `*zerolog.Logger` as a struct field; there should be none. Grep for `log.With().` outside of a test or test helper; the engine attaches the strategy name automatically. Count `Info()` calls per tick -- more than one or two means the log is going to be too noisy for a useful backtest. Verify the lint suite includes `zerologlint`; the pvbt repo enforces it.

## The --preset naming footgun

This is a real, present-day pvbt code bug. The strategy parameter name (used by `describe` output and `--preset` lookup) and the cobra CLI flag name are derived through two different code paths that disagree for any multi-word field without an explicit `pvbt` tag. The result is that `--preset` silently fails to apply values to the misnamed fields, and the strategy runs with defaults instead of preset values, with no warning.

The two derivations:

```go
// engine/parameter.go: parameter name (used by --preset lookup and describe).
name := field.Tag.Get("pvbt")
if name == "" {
    name = strings.ToLower(field.Name) // RiskOn -> "riskon"
}

// cli/flags.go: cobra flag name (used for --risk-on at the CLI).
name := field.Tag.Get("pvbt")
if name == "" {
    name = toKebabCase(field.Name) // RiskOn -> "risk-on"
}
```

So a field declared `RiskOn universe.Universe` with no `pvbt` tag has parameter name `riskon` and cobra flag name `--risk-on`. When the user runs `--preset Classic`, the preset resolver looks for parameter `riskon`, the cobra layer registered `--risk-on`, and the lookup quietly returns nothing for that field -- the default is used instead of the preset value. There is no error message and no warning. The strategy runs with subtly wrong parameters.

**Symptom:** Strategy struct with multi-word field names (`RiskOn`, `RiskOff`, `Lookback` is fine because it is a single word), `suggest:` tags declaring presets, but no explicit `pvbt:` tag on the same field.

```go
// BAD: missing pvbt tag, multi-word field name.
type Strategy struct {
    RiskOn   universe.Universe `desc:"Risk-on assets"  default:"SPY,EFA"  suggest:"Classic=VFINX,PRIDX|Modern=SPY,QQQ"`
    RiskOff  universe.Universe `desc:"Risk-off asset"  default:"SHY"      suggest:"Classic=VUSTX|Modern=TLT"`
    Lookback int               `desc:"Lookback months" default:"6"        suggest:"Classic=12|Modern=6"`
}
```

`./mystrategy backtest --preset Classic` will silently fail to set `risk-on` and `risk-off` (because the resolver looks for `riskon` and `riskoff`), but **will** correctly set `lookback` (because the lowercase and kebab-case derivations are identical for single-word names). The strategy runs with `risk-on=SPY,EFA` (default) and `lookback=12` (preset) -- a parameter combination the author never intended.

**Fix:** Always set the `pvbt` tag explicitly on every field, with the exact name you want users to type. Pick kebab-case for multi-word names. With an explicit tag, both code paths use the same string and the divergence vanishes.

```go
// GOOD: explicit pvbt tag on every field.
type Strategy struct {
    RiskOn   universe.Universe `pvbt:"risk-on"  desc:"Risk-on assets"  default:"SPY,EFA" suggest:"Classic=VFINX,PRIDX|Modern=SPY,QQQ"`
    RiskOff  universe.Universe `pvbt:"risk-off" desc:"Risk-off asset"  default:"SHY"     suggest:"Classic=VUSTX|Modern=TLT"`
    Lookback int               `pvbt:"lookback" desc:"Lookback months" default:"6"       suggest:"Classic=12|Modern=6"`
}
```

**How to detect in review:** Grep the strategy struct for any field that has a `suggest:` tag but no `pvbt:` tag. That field is broken under `--preset` unless the field name happens to be a single lowercase word. More broadly: any multi-word exported field on a strategy struct should carry an explicit `pvbt:` tag, and the reviewer should flag the absence as a defect even if the strategy does not (yet) declare presets, because the next person to add a preset will trip over the bug. Additionally, after adding presets, run `./mystrategy describe --json` and confirm the keys in the `suggestions` map exactly match the keys in the `parameters` list and the cobra flag names (you can dump the latter with `./mystrategy backtest --help`). Any mismatch is the bug.

## See also

- [./strategy-api.md](./strategy-api.md) -- the `Strategy` and `Descriptor` interfaces, lifecycle, and `cli.Run` surface area.
- [./scheduling.md](./scheduling.md) -- tradecron expressions, warmup, time zone semantics, schedule pitfalls.
- [./universes.md](./universes.md) -- static, index, rated, and us-tradable universes; the survivorship-bias-free way to fetch members.
- [./data-frames.md](./data-frames.md) -- DataFrame shape, slicing, arithmetic, and aux state.
- [./signals-and-weighting.md](./signals-and-weighting.md) -- signal pipeline, selectors that mutate in place, weighting functions.
- [./portfolio-and-batch.md](./portfolio-and-batch.md) -- read-only portfolio queries and the batch API for orders and annotations.
- [./parameters-and-presets.md](./parameters-and-presets.md) -- parameter struct tags, preset declaration, and the canonical writeup of the naming divergence.
- [./testing-strategies.md](./testing-strategies.md) -- snapshot capture and replay, Ginkgo conventions, deterministic tests.
