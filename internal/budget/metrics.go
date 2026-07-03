package budget

import (
	"sync/atomic"
	"time"
)

type Metrics struct {
	BudgetCheckAllowed  atomic.Int64
	BudgetCheckDenied   atomic.Int64
	BudgetCheckFailOpen atomic.Int64
	FailOpenTotal       atomic.Int64
	SettleSuccess       atomic.Int64
	SettleRetry         atomic.Int64
	MissingUsage        atomic.Int64

	reserveCount atomic.Int64
	reserveSumNs atomic.Int64
}

func (m *Metrics) RecordReserve(duration time.Duration) {
	m.reserveCount.Add(1)
	m.reserveSumNs.Add(duration.Nanoseconds())
}

func (m *Metrics) IncAllowed()  { m.BudgetCheckAllowed.Add(1) }
func (m *Metrics) IncDenied()   { m.BudgetCheckDenied.Add(1) }
func (m *Metrics) IncFailOpen() { m.FailOpenTotal.Add(1); m.BudgetCheckFailOpen.Add(1) }

func (m *Metrics) IncSettleSuccess() { m.SettleSuccess.Add(1) }
func (m *Metrics) IncSettleRetry()   { m.SettleRetry.Add(1) }
func (m *Metrics) IncMissingUsage()  { m.MissingUsage.Add(1) }

func (m *Metrics) ReserveAvgMs() float64 {
	count := m.reserveCount.Load()
	if count == 0 {
		return 0
	}
	return float64(m.reserveSumNs.Load()) / float64(count) / 1e6
}
