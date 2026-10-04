// Package worker tem os processos de fundo: o publicador da outbox e a
// retomada de pendências. Rodam em várias instâncias ao mesmo tempo sem
// problema, porque quem coordena é o banco (SKIP LOCKED + lease).
package worker
