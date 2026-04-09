// Package main is the DEGRADED fixture for the pvbt-strategy-reviewer
// validation run. Three issues are intentionally planted -- one per review
// pass -- so the simulated reviewer's three findings can be compared against
// the clean baseline in momentum-rotation.go.
package main

import (
	"context"
	"fmt"

	"github.com/penny-vault/pvbt/asset"
	"github.com/penny-vault/pvbt/cli"
	"github.com/penny-vault/pvbt/data"
	"github.com/penny-vault/pvbt/engine"
	"github.com/penny-vault/pvbt/portfolio"
	"github.com/penny-vault/pvbt/universe"
	"github.com/rs/zerolog"
)

// MomentumRotation rotates monthly into whichever risk-on name has the
// highest trailing return over the lookback window.
type MomentumRotation struct {
	// PLANTED (Pass 3: quant red flag): static mega-cap list is survivorship
	// biased -- META, GOOG, AAPL, AMZN, MSFT did not all exist or look like
	// "the winners" over the full backtest window.
	RiskOn   universe.Universe `pvbt:"risk-on"  desc:"Assets to rotate between"    default:"AAPL,MSFT,GOOG,AMZN,META"`
	RiskOff  universe.Universe `pvbt:"risk-off" desc:"Safe-haven asset"            default:"SHY"`
	Lookback int               `pvbt:"lookback" desc:"Momentum lookback in months" default:"6"`
}

// Name returns the strategy identifier used in logging and CLI output.
func (s *MomentumRotation) Name() string { return "momentum-rotation" }

// Setup is a no-op.
func (s *MomentumRotation) Setup(_ *engine.Engine) {}

// Describe declares the schedule, benchmark, and warmup.
func (s *MomentumRotation) Describe() engine.StrategyDescription {
	return engine.StrategyDescription{
		Schedule:  "@monthend",
		Benchmark: "SPY",
		Warmup:    126,
	}
}

// Compute fetches a lookback window of closes, computes total return over
// the window, and rotates into the single best performer (or risk-off if
// none is positive).
func (s *MomentumRotation) Compute(ctx context.Context, eng *engine.Engine, port portfolio.Portfolio, batch *portfolio.Batch) error {
	log := zerolog.Ctx(ctx)

	// PLANTED (Pass 1: correctness): silent failure -- the error is logged
	// but the function returns nil, pretending everything is fine.
	riskOnDF, err := s.RiskOn.Window(ctx, portfolio.Months(s.Lookback), data.MetricClose)
	if err != nil {
		log.Error().Err(err).Msg("data fetch failed")
		return nil
	}
	if riskOnDF.Len() < 2 {
		log.Debug().Int("len", riskOnDF.Len()).Msg("insufficient risk-on history; skipping tick")
		return nil
	}

	// PLANTED (Pass 2: idiom): hand-rolled selection and weighting instead of
	// the canonical pipeline. The canonical form is:
	//     portfolio.MaxAboveZero(data.MetricClose, riskOffDF).Select(momentum)
	//     plan, err := portfolio.EqualWeight(momentum)
	momentum := riskOnDF.Pct(riskOnDF.Len() - 1).Last()

	var best asset.Asset
	bestRet := 0.0
	for _, ast := range momentum.AssetList() {
		ret := momentum.Value(ast, data.MetricClose)
		if ret > bestRet {
			bestRet = ret
			best = ast
		}
	}

	riskOffDF, err := s.RiskOff.At(ctx, data.MetricClose)
	if err != nil {
		return fmt.Errorf("risk-off snapshot fetch: %w", err)
	}

	members := map[asset.Asset]float64{}
	if bestRet > 0 {
		members[best] = 1.0
	} else {
		for _, ast := range riskOffDF.AssetList() {
			members[ast] = 1.0
			break
		}
	}

	if err := batch.RebalanceTo(ctx, portfolio.Allocation{
		Date:    eng.CurrentDate(),
		Members: members,
	}); err != nil {
		return fmt.Errorf("rebalance: %w", err)
	}

	return nil
}

func main() {
	cli.Run(&MomentumRotation{})
}
