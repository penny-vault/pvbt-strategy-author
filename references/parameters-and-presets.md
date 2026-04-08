_Last verified against pvbt 0.6.0._

# Parameters and Presets

Strategy parameters are exported struct fields on the strategy type. Before the first `Compute` call, the engine reflects over the struct, populates each field from struct tags or CLI flags, and then calls `Setup`. Every exported field of a supported type becomes a CLI flag automatically. No external configuration files are required.

```go
type MomentumRotation struct {
    RiskOn   universe.Universe `pvbt:"risk-on"  desc:"Assets to rotate between" default:"SPY,EFA,EEM" suggest:"Classic=VFINX,PRIDX|Modern=SPY,QQQ"`
    RiskOff  universe.Universe `pvbt:"risk-off" desc:"Safe-haven asset"         default:"SHY"        suggest:"Classic=VUSTX|Modern=TLT"`
    Lookback int               `pvbt:"lookback" desc:"Momentum lookback months"  default:"6"`
}
```

The struct above declares three CLI flags (`--risk-on`, `--risk-off`, `--lookback`), three default values, and two named presets ("Classic" and "Modern") that the user can pick at runtime with `--preset`.

## Supported types

The engine recognizes seven field types. Anything else is silently ignored by parameter reflection (the engine logs a warning for unsupported field kinds during CLI flag registration). The supported types are:

| Go type | Default tag format | CLI example |
|---------|--------------------|-------------|
| `int` | Decimal integer | `--lookback 6` |
| `float64` | Decimal number | `--threshold 0.02` |
| `string` | Plain text | `--mode momentum` |
| `bool` | `true` or `false` | `--rebalance-quarterly` |
| `time.Duration` | Go duration string (`720h`, `15m`, `1h30m`) | `--hold-period 720h` |
| `asset.Asset` | Single ticker symbol | `--benchmark SPY` |
| `universe.Universe` | Comma-separated tickers | `--risk-on SPY,EFA,EEM` |

Key notes on the type-specific behavior:

- `asset.Asset` defaults are resolved through the engine's `AssetProvider` (`eng.Asset(ticker)`) during hydration. An unknown ticker will fail there.
- `universe.Universe` defaults are split on commas, each ticker is resolved via `eng.Asset`, and the result is wrapped in a static universe via `eng.Universe(...)`. CLI input goes through the same flow but uses `universe.NewStatic` directly.
- `time.Duration` defaults are parsed with `time.ParseDuration`, so any unit Go accepts (`ns`, `us`, `ms`, `s`, `m`, `h`) is valid.
- Slices, maps, pointers, and custom struct types are not supported. If you need a list of something other than tickers, take a comma-separated `string` and parse it yourself in `Setup`.
- Fields whose type is `engine.Strategy` (or a pointer to one) are skipped by parameter reflection -- those are child strategies, not parameters. See [./strategy-api.md](./strategy-api.md) for meta-strategy composition.

Unexported fields (lowercase first letter) are always skipped. This is how you keep internal state on the strategy struct without exposing it as a flag.

## Struct tags

Four struct tags control how a field is exposed:

| Tag | Purpose |
|-----|---------|
| `pvbt` | Parameter name. Used as the key in the `describe` output and as the lookup key for `--preset`. |
| `desc` | Description shown in `describe` and used as cobra flag help text. |
| `default` | Default value, parsed into the field's type during hydration. |
| `suggest` | Pipe-delimited list of named presets. Format: `PresetName=value\|OtherPreset=value`. |

Full example using all four tags:

```go
type MyStrategy struct {
    Lookback int `pvbt:"lookback" desc:"Momentum lookback in months" default:"6" suggest:"Fast=3|Classic=6|Slow=12"`
}
```

Hydration rules (see `engine/hydrate.go`):

1. If a field is already non-zero when hydration runs (because the caller pre-set it, or a CLI flag populated it), the `default` tag is ignored. CLI flags always win.
2. Otherwise, the `default` tag is parsed into the field's type. Parse failures are fatal; the engine returns an error like `hydrate MyStrategy.Lookback: parsing int "foo": ...`.
3. Universe fields that were pre-set are re-wired through the engine's data source so data fetching works -- you do not need to think about this in strategy code.

## Naming

The auto-derived parameter name is **the lowercase field name**, not kebab-case. `engine.StrategyParameters` (the function backing `describe` and `--preset` lookup) computes the name as:

```go
name := field.Tag.Get("pvbt")
if name == "" {
    name = strings.ToLower(field.Name)
}
```

So a field declared as `RiskOn` with no `pvbt` tag becomes the parameter name `riskon`, not `risk-on`. The codebase contains a separate code path in `cli/flags.go` that registers cobra flags using kebab-case (`toKebabCase("RiskOn") = "risk-on"`), so the cobra flag and the parameter name disagree when the `pvbt` tag is omitted. Presets and `describe --json` use the lowercase name; the actual cobra flag uses kebab-case. This divergence is a known footgun.

`docs/strategy-guide.md` claims kebab-case is the rule, but the engine code is authoritative for `describe` and `--preset`. To avoid the discrepancy entirely:

**Always set the `pvbt` tag explicitly.** Pick the exact name you want users to type. For multi-word parameters, kebab-case (`risk-on`, `vol-target`, `rebalance-band`) is the conventional choice and matches the CLI examples in the docs. With an explicit `pvbt` tag, both the cobra flag and the parameter name in `describe` use the same string.

Naming guidance:

- Use lowercase. Tag values are passed through to cobra unchanged, and mixed-case flags are awkward to type.
- Use kebab-case for multi-word names (`hold-period`, not `holdperiod` or `hold_period`).
- Use full words, not abbreviations. `lookback` beats `lkbk`. `volatility-target` beats `vol-tgt`.
- Match the user's mental model, not the field name. A field named `Lookback` describing a number of months should be `lookback-months` if the unit is not obvious from context.
- Avoid leading dashes or characters cobra reserves for itself.

## Presets

A preset is a named bundle of parameter values that the user can apply with `--preset Name`. Presets are declared with the `suggest` tag on individual fields. The same preset name can appear on multiple fields; each field contributes its own value.

Format:

```
suggest:"PresetName=value|OtherPreset=value|ThirdPreset=value"
```

Multiple fields participating in the same preset:

```go
type ADM struct {
    RiskOn   universe.Universe `pvbt:"risk-on"  default:"VOO,SCZ" suggest:"Classic=VFINX,PRIDX|Modern=SPY,QQQ"`
    RiskOff  universe.Universe `pvbt:"risk-off" default:"TLT"     suggest:"Classic=VUSTX|Modern=TLT"`
    Lookback int               `pvbt:"lookback" default:"6"       suggest:"Classic=12|Modern=6"`
}
```

Picking "Classic" populates `risk-on=VFINX,PRIDX`, `risk-off=VUSTX`, `lookback=12` in one shot:

```bash
./adm backtest --preset Classic
```

Resolution rules (see `cli/preset.go`):

- The CLI looks up the preset name in the union of `suggest` tags across all fields. Unknown names produce an error listing the available presets.
- Each preset value is applied to the matching parameter only if the user did not also set that flag explicitly. Explicit flags always win over presets, just as CLI flags always win over `default` tags.
- The lookup keys come from `engine.StrategyParameters`, so they use the `pvbt` tag value (or the lowercase field name if no tag is set). This is another reason to set the `pvbt` tag explicitly: presets silently fail to apply when the parameter name and the cobra flag name disagree.
- A preset does not need to set every field. Fields not mentioned by the preset keep their `default` (or whatever the user passed on the CLI).

## When to expose a parameter

Every exported field on the strategy struct becomes a CLI flag. That is convenient, and it is also the main way strategies become unusable to outsiders. A good strategy exposes the values a thoughtful user would plausibly want to vary, and hard-codes everything else.

Expose:

- **Lookback windows.** A 12-month momentum lookback might be a 6-month or 24-month lookback in someone else's research. This is the canonical knob.
- **Universes.** Let users swap `SPY,EFA,EEM` for `VTI,VEA,VWO` without recompiling.
- **Rebalance bands and thresholds.** Drift tolerance, signal cutoffs, the dead zone around zero.
- **Position sizing knobs.** Number of holdings, weight cap, volatility target.
- **Switches between documented modes.** A `bool` that picks between two well-defined behaviors.

Do not expose:

- **Numbers chosen by trial and error during development.** If you can't write a sentence explaining what a value means and what changing it should do, it should not be a flag.
- **Implementation magic numbers.** Smoothing constants, epsilon guards, internal sample sizes used to make a calculation behave well. These belong as Go constants in the strategy file.
- **Things the engine already controls.** The schedule lives in `Describe()`, not in a flag. The benchmark belongs in `Describe()` or `Setup`. Do not re-expose them.
- **Anything you would not know how to defend in code review.** A flag is a promise that the value matters and the user can reason about it.

A useful test: write the help text first. If `desc:"..."` does not say something concrete about how the value affects behavior and how a user would choose it, the field should not be a parameter.

## Overfitting and parameter count

Every exposed parameter is a tuning knob. Every tuning knob is an opportunity to overfit -- to pick the value that looked best in backtest, not the value that will hold out of sample. The relationship is roughly multiplicative: a strategy with 8 free parameters and 5 plausible values each has 390,625 backtests in its search space, and the best of those is almost certainly an artifact of noise.

Practical rules:

- **Prefer fewer parameters.** Aim for the smallest set that captures the essential decisions of the strategy. Three to five parameters is plenty for most ideas. More than ten is a red flag.
- **Defaults must be defensible.** If the only justification for a default is "it's the value that won the grid search," the parameter is overfit by construction. Defaults should come from theory, the original paper, or a value the strategy would still be tradeable at.
- **Use presets, not knobs, to capture variants.** If you have three plausible parameterizations, expose them as `Aggressive`, `Moderate`, and `Conservative` presets backed by sensible parameter combinations rather than letting users grid-search. Presets are an opinionated design statement; raw flags are not.
- **Resist adding parameters mid-development.** A new flag every time you encounter a tradeoff produces a strategy that fits its own backtest beautifully and nothing else.
- **Run sweeps in [study mode](./strategy-api.md), not by hand.** When you do need to explore a parameter, use the `study` subcommand and look at the distribution of outcomes, not just the best one. A strategy whose performance collapses outside a narrow parameter band is overfit.

Strategies with many parameters should treat the documentation burden as part of the cost: each flag deserves a description that says what it controls, why the default is chosen, and what range is reasonable.

## describe output

`describe` is the canonical way to verify that your parameter declarations were picked up correctly. Run it whenever you change a struct tag.

Human-readable form:

```bash
./mystrategy describe
```

```
mystrategy v1.0.0
A momentum rotation strategy.
Source: https://example.com/...
Schedule:  @monthend
Benchmark: SPY (suggested)

Parameters:
  risk-on    Assets to rotate between        default: SPY,EFA,EEM
  risk-off   Safe-haven asset                default: SHY
  lookback   Momentum lookback months        default: 6

Presets:
  Classic   risk-on=VFINX,PRIDX  risk-off=VUSTX  lookback=12
  Modern    risk-on=SPY,QQQ      risk-off=TLT    lookback=6
```

JSON form (built from `engine.DescribeStrategy`, which serializes a `StrategyInfo`):

```bash
./mystrategy describe --json
```

```json
{
  "name": "mystrategy",
  "shortcode": "mystrategy",
  "description": "A momentum rotation strategy.",
  "source": "https://example.com/...",
  "version": "1.0.0",
  "schedule": "@monthend",
  "benchmark": "SPY",
  "riskFree": "DGS3MO",
  "warmup": 252,
  "parameters": [
    {"name": "risk-on",  "description": "Assets to rotate between", "type": "universe.Universe", "default": "SPY,EFA,EEM"},
    {"name": "risk-off", "description": "Safe-haven asset",         "type": "universe.Universe", "default": "SHY"},
    {"name": "lookback", "description": "Momentum lookback months", "type": "int",                "default": "6"}
  ],
  "suggestions": {
    "Classic": {"risk-on": "VFINX,PRIDX", "risk-off": "VUSTX", "lookback": "12"},
    "Modern":  {"risk-on": "SPY,QQQ",     "risk-off": "TLT",   "lookback": "6"}
  }
}
```

What to verify in the JSON:

- Every parameter you intended to expose is in the `parameters` list, with the right `name`, `description`, `type`, and `default`.
- The `suggestions` map contains every preset you declared, and each preset references the parameter names you expected. If a preset references `riskon` but the cobra flag is `--risk-on`, you forgot the explicit `pvbt` tag.
- `riskFree` is `DGS3MO` (the engine fills this in unconditionally).
- `schedule`, `benchmark`, and `warmup` reflect what you declared in `Describe()`.
- The `name` is the value returned by `Strategy.Name()`, not the Go type name.

`describe` does not require `Setup` to have run, and it does not require a data provider. It is a pure reflection-driven dump of the strategy struct, which makes it the fastest feedback loop for parameter authoring. Run it after every tag change.

## See also

- [./strategy-api.md](./strategy-api.md) -- the `Strategy` and `Descriptor` interfaces that parameters attach to, plus child-strategy composition.
- [./common-pitfalls.md](./common-pitfalls.md) -- catalog of mistakes when writing pvbt strategies, including the parameter-name divergence between `cli/flags.go` and `engine/parameter.go`.
