_Last verified against pvbt 0.6.0._

# Testing Strategies

A pvbt strategy is just Go code, so its tests are just Go tests. The pvbt project standardizes on Ginkgo/Gomega in BDD style and on **snapshot-based data** instead of hand-rolled mocks. Tests must be deterministic, hermetic, and runnable offline.

This reference shows the conventional patterns for testing strategies built with pvbt.

## Snapshot-based testing

A snapshot is a SQLite file that records every data request and response from a real backtest. Once captured, the snapshot can be replayed in tests with no network or database access.

### Capture a snapshot

Every binary built with `cli.Run` ships a `snapshot` subcommand. Run the strategy with the same parameters you intend to test against:

```bash
./momentum-rotation snapshot \
    --start 2023-01-01 \
    --end 2024-01-01 \
    --output testdata/snapshot.db
```

This runs a full backtest under the recording provider and writes every fetched price, asset, index member, rating, and market holiday into the output SQLite file. The default output path is `pv-data-snapshot-{strategy}-{start}-{end}.db`; pass `--output` to control where the file lands. The conventional location is `testdata/` next to the test file, because Go's tooling already excludes `testdata/` from package builds and `go vet`.

Commit the resulting `.db` file to version control. It is the test fixture; the test depends on it being byte-stable.

### Regenerate after a strategy change

The snapshot only contains data the strategy actually requested. If you add a new metric, change the universe, or extend the date range, rerun the same `snapshot` command and commit the updated file. Tests will fail loudly if a request misses the snapshot, so divergence is caught immediately rather than silently.

## Replaying a snapshot

The replay API is `data.NewSnapshotProvider`, defined in `data/snapshot_provider.go`. The returned `*data.SnapshotProvider` satisfies every data provider interface the engine uses (`BatchProvider`, `AssetProvider`, `IndexProvider`, `RatingProvider`, `HolidayProvider`), so a single value wires up the whole engine.

```go
package mystrategy_test

import (
    "context"
    "time"

    . "github.com/onsi/ginkgo/v2"
    . "github.com/onsi/gomega"

    "github.com/penny-vault/pvbt/data"
    "github.com/penny-vault/pvbt/engine"
    "github.com/penny-vault/pvbt/portfolio"
)

var _ = Describe("MomentumRotation", func() {
    It("produces positive returns over the test period", func() {
        ctx := context.Background()

        snap, err := data.NewSnapshotProvider("testdata/snapshot.db")
        Expect(err).NotTo(HaveOccurred())
        defer snap.Close()

        startDate := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
        endDate := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

        strategy := &MomentumRotation{}

        acct := portfolio.New(
            portfolio.WithCash(100_000, startDate),
            portfolio.WithAllMetrics(),
        )

        eng := engine.New(strategy,
            engine.WithDataProvider(snap),
            engine.WithAssetProvider(snap),
            engine.WithAccount(acct),
        )

        result, err := eng.Backtest(ctx, startDate, endDate)
        Expect(err).NotTo(HaveOccurred())

        summary, err := result.Summary()
        Expect(err).NotTo(HaveOccurred())
        Expect(summary.TWRR).To(BeNumerically(">", 0))
    })
})
```

`SnapshotProvider` opens the database read-only via `PRAGMA query_only = ON`, so concurrent test runs cannot corrupt the fixture. Always `Close()` the provider when the test finishes (a `defer` in the `It` block is fine).

A few notes that matter when wiring the engine:

- Pass the same provider to both `WithDataProvider` and `WithAssetProvider`. The snapshot satisfies both interfaces.
- The snapshot is timezone-aware. Date-only entries in the database are decoded at 16:00 America/New_York to match the timestamps the live PV data provider emits, so tradecron `@close` directives line up correctly.
- The snapshot provider also serves market holidays, so tradecron does not need a separate holiday source.

## Ginkgo conventions

The pvbt repository uses Ginkgo v2 with Gomega for every package. Three conventions are non-negotiable:

1. **One `*_suite_test.go` per package.** It runs the suite and routes logs.
2. **Zerolog goes to `GinkgoWriter`.** Test output stays clean unless a spec fails.
3. **BDD nesting** -- `Describe` for the unit under test, `Context` for the scenario, `It` for the assertion.

A minimal suite file:

```go
// strategy_suite_test.go
package mystrategy_test

import (
    "testing"

    . "github.com/onsi/ginkgo/v2"
    . "github.com/onsi/gomega"
    "github.com/penny-vault/pvbt/tradecron"
    "github.com/rs/zerolog/log"
)

var _ = BeforeSuite(func() {
    // The snapshot provider supplies holidays at runtime, but tradecron
    // panics if no calendar is loaded before the first schedule parse.
    tradecron.SetMarketHolidays(nil)
})

func TestMyStrategy(t *testing.T) {
    RegisterFailHandler(Fail)

    // Send all zerolog output to GinkgoWriter so logs only surface on failure.
    log.Logger = log.Output(GinkgoWriter)

    RunSpecs(t, "MyStrategy Suite")
}
```

A spec file alongside it:

```go
// strategy_test.go
package mystrategy_test

import (
    . "github.com/onsi/ginkgo/v2"
    . "github.com/onsi/gomega"
)

var _ = Describe("MomentumRotation", func() {
    Context("when the universe is empty", func() {
        It("returns no orders without erroring", func() {
            // ... arrange, act, assert ...
            Expect(true).To(BeTrue())
        })
    })

    Context("when momentum is positive across the lookback window", func() {
        It("rotates fully into the highest-ranked asset", func() {
            // ... arrange, act, assert ...
        })
    })
})
```

Run the suite with `ginkgo run -race ./...` or via `make test` (the project Makefile already wires the right flags).

## Regression test pattern

A regression test runs a short backtest over a fixed window from a snapshot, then asserts on a summary metric within a tolerance. The point is to catch unintended numerical drift in the strategy's returns when refactoring.

```go
package mystrategy_test

import (
    "context"
    "math"
    "time"

    . "github.com/onsi/ginkgo/v2"
    . "github.com/onsi/gomega"

    "github.com/penny-vault/pvbt/data"
    "github.com/penny-vault/pvbt/engine"
    "github.com/penny-vault/pvbt/portfolio"
)

var _ = Describe("MomentumRotation regression", func() {
    It("matches the recorded TWRR within 1bp", func() {
        ctx := context.Background()

        snap, err := data.NewSnapshotProvider("testdata/snapshot.db")
        Expect(err).NotTo(HaveOccurred())
        defer snap.Close()

        startDate := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
        endDate := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

        acct := portfolio.New(
            portfolio.WithCash(100_000, startDate),
            portfolio.WithAllMetrics(),
        )

        eng := engine.New(&MomentumRotation{Lookback: 6},
            engine.WithDataProvider(snap),
            engine.WithAssetProvider(snap),
            engine.WithAccount(acct),
        )

        result, err := eng.Backtest(ctx, startDate, endDate)
        Expect(err).NotTo(HaveOccurred())

        summary, err := result.Summary()
        Expect(err).NotTo(HaveOccurred())

        // Golden value captured from the previous green run.
        const expectedTWRR = 0.1842
        const tolerance = 0.0001 // 1 basis point

        Expect(math.Abs(summary.TWRR-expectedTWRR)).To(BeNumerically("<", tolerance),
            "TWRR drifted: got %.6f want %.6f", summary.TWRR, expectedTWRR)
    })
})
```

Tips for keeping regression tests stable:

- Pin every parameter explicitly in the test (`Lookback: 6`, etc.). Do not rely on struct-tag defaults; a future tag change must not silently invalidate the test.
- Pin the start and end dates as Go literals rather than reading from the snapshot metadata.
- When an expected value legitimately needs to change, update the constant in a single commit alongside the strategy change so reviewers can see the drift.
- Pick a tolerance that is wide enough to absorb legitimate floating-point reordering (1 bp on TWRR is usually safe) but tight enough to catch real logic regressions.

For finer-grained assertions, walk the per-step equity curve directly. The result is a `Portfolio`, and `result.PerfData().Column(perfA, data.PortfolioEquity)` returns the contiguous `[]float64` equity series. A regression test can assert on the final element, the maximum drawdown, or the entire series against a golden slice.

## Do not mock data providers

**Use snapshots, not hand-rolled mocks.** Mocking `BatchProvider` or `AssetProvider` looks easy in isolation, but every mock is a private re-implementation of the data contract and they drift from the real providers in subtle ways: missing metrics on a particular date, wrong timestamp normalization, a metric column the strategy started consuming after the mock was written. This project has been bitten by mock-driven tests passing while real backtests broke.

A snapshot is a recording of an actual data provider, byte-for-byte. By construction it cannot disagree with reality. If the strategy starts asking for new data, the snapshot misses, and the test fails noisily on the new request -- which is exactly the signal you want.

There is one narrow exception inside the engine package itself: `engine/backtest_test.go` defines a tiny `mockAssetProvider` because those tests exercise the engine machinery and need to inject synthetic asset lists. Strategy tests should not follow that pattern. If you find yourself reaching for a mock, capture a snapshot instead.

## Offline guarantee

Strategy tests must never touch the network and must never read from any database other than the committed snapshot. Concretely:

- The snapshot `.db` file lives in `testdata/` and is committed to the repository alongside the test that consumes it. It is the only data source the test is allowed to open.
- Tests do not read environment variables that point at live data providers (`PV_DATA_URL`, database DSNs, etc.). The snapshot provider needs no credentials.
- Tests do not call `cli.Run` or any code path that constructs the default data stack. They build the engine manually with `engine.New` and explicit `WithDataProvider` / `WithAssetProvider` options pointed at the snapshot.
- If a snapshot file is too large to commit comfortably, the convention is to add a `make snapshots` Makefile target that regenerates fixtures from the live providers, and to gate tests on the file's presence with a clear error message rather than a silent skip. Never silently fall back to a live provider.

Run the suite under `make test`. If a strategy test ever opens a network socket, the team treats it as a bug.

## See also

- [common-pitfalls.md](./common-pitfalls.md) -- mistakes that snapshot tests catch and mistakes they do not.
