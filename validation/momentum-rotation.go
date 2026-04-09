// Package main implements a canonical dual-momentum rotation strategy used
// as a clean baseline fixture for the pvbt-strategy-reviewer agent validation.
package main

import (
	"context"
	"fmt"

	"github.com/penny-vault/pvbt/cli"
	"github.com/penny-vault/pvbt/data"
	"github.com/penny-vault/pvbt/engine"
	"github.com/penny-vault/pvbt/portfolio"
	"github.com/penny-vault/pvbt/universe"
	"github.com/rs/zerolog"
)

// MomentumRotation rotates monthly into whichever risk-on ETF has the highest
// trailing return over the lookback window, falling back to a short-duration
// treasury fund when no risk-on asset has positive momentum.
type MomentumRotation struct {
	RiskOn   universe.Universe `pvbt:"risk-on"  desc:"Assets to rotate between"    default:"SPY,EFA,EEM"`
	RiskOff  universe.Universe `pvbt:"risk-off" desc:"Safe-haven asset"            default:"SHY"`
	Lookback int               `pvbt:"lookback" desc:"Momentum lookback in months" default:"6"`
}

// Name returns the strategy identifier used in logging and CLI output.
func (s *MomentumRotation) Name() string { return "momentum-rotation" }

// Setup is a no-op: everything this strategy needs is declared via struct tags
// and Describe.
func (s *MomentumRotation) Setup(_ *engine.Engine) {}

// Describe declares the schedule, benchmark, and warmup. Warmup is sized to
// cover the longest lookback the strategy reads (6 months of trading days).
func (s *MomentumRotation) Describe() engine.StrategyDescription {
	return engine.StrategyDescription{
		Schedule:  "@monthend",
		Benchmark: "SPY",
		Warmup:    126,
	}
}

// Compute fetches a lookback window of closes for the risk-on universe,
// computes total return over the window, and rotates into the single best
// performer (or risk-off if none is positive).
func (s *MomentumRotation) Compute(ctx context.Context, eng *engine.Engine, port portfolio.Portfolio, batch *portfolio.Batch) error {
	log := zerolog.Ctx(ctx)

	riskOnDF, err := s.RiskOn.Window(ctx, portfolio.Months(s.Lookback), data.MetricClose)
	if err != nil {
		return fmt.Errorf("risk-on window fetch: %w", err)
	}
	if riskOnDF.Len() < 2 {
		log.Debug().Int("len", riskOnDF.Len()).Msg("insufficient risk-on history; skipping tick")
		return nil
	}

	momentum := riskOnDF.Pct(riskOnDF.Len() - 1).Last()

	riskOffDF, err := s.RiskOff.At(ctx, data.MetricClose)
	if err != nil {
		return fmt.Errorf("risk-off snapshot fetch: %w", err)
	}

	portfolio.MaxAboveZero(data.MetricClose, riskOffDF).Select(momentum)

	plan, err := portfolio.EqualWeight(momentum)
	if err != nil {
		return fmt.Errorf("equal-weight plan: %w", err)
	}

	if err := batch.RebalanceTo(ctx, plan...); err != nil {
		return fmt.Errorf("rebalance: %w", err)
	}

	return nil
}

func main() {
	cli.Run(&MomentumRotation{})
}
