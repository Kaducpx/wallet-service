package app

import "time"

// Metrics são as métricas que os casos de uso registram (interface para não
// depender do Prometheus aqui).
type Metrics interface {
	WagerResult(channel, status string, replay bool)
	DuplicateDetected(channel, kind string)
	ConcurrencyConflict(operation string)
	ProcessingDuration(channel string, d time.Duration)
	ReconciliationDivergence()
}

// NopMetrics não faz nada (testes unitários).
type NopMetrics struct{}

func (NopMetrics) WagerResult(string, string, bool)         {}
func (NopMetrics) DuplicateDetected(string, string)         {}
func (NopMetrics) ConcurrencyConflict(string)               {}
func (NopMetrics) ProcessingDuration(string, time.Duration) {}
func (NopMetrics) ReconciliationDivergence()                {}

// FaultInjector simula quedas nos testes de recuperação. Em produção não faz
// nada.
type FaultInjector interface {
	Hit(point string) error
}

type NopFaults struct{}

func (NopFaults) Hit(string) error { return nil }
