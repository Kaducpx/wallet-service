package app

import (
	"log/slog"
	"time"

	"wallet-service/internal/domain"
)

// Canais de entrada (usados em métricas e logs).
const (
	ChannelHTTP    = "http"
	ChannelSQS     = "sqs"
	ChannelPending = "pending-worker"
)

// Pontos de injeção de falha usados pelos testes de recuperação.
const (
	FaultWagerAfterCommit = "wager.after_commit"
	FaultWagerSubmit      = "wager.submit"
)

// Quantas vezes refazer a transação quando perde uma disputa de concorrência.
const maxConflictRetries = 5

// WagerService é usado pelo HTTP, pelo SQS e pelo worker de pendências. Um
// serviço só para todos garante as mesmas regras em todos os canais.
//
// Os campos são interfaces para não depender de Postgres ou Prometheus
// direto; quem monta as peças é o Fx.
type WagerService struct {
	tx      TxRunner             // abre transações SQL
	policy  domain.PendingPolicy // regras de espera por referência
	metrics Metrics
	faults  FaultInjector // no-op em produção; usado nos testes de queda
	log     *slog.Logger
	now     func() time.Time // relógio substituível (facilita testes)
}

func NewWagerService(tx TxRunner, policy domain.PendingPolicy, metrics Metrics, faults FaultInjector, log *slog.Logger) *WagerService {
	return &WagerService{tx: tx, policy: policy, metrics: metrics, faults: faults, log: log, now: Now}
}

// SubmitCommand é uma operação externa pronta para processamento.
type SubmitCommand struct {
	Op             WagerOperation // dados já validados (ParseWagerRequest)
	IdempotencyKey string         // header Idempotency-Key (HTTP) ou data.idempotencyKey (SQS)
	CorrelationID  string         // rastreia a operação nos logs e eventos
	CausationID    string         // messageId no SQS
	Channel        string         // "http" ou "sqs" (métricas e logs)
	// Só no SQS: o registro da inbox vai no mesmo commit do resto.
	Inbox *InboxRecord
}

// SubmitResult traz a transação gravada (nova ou a original, se for replay).
type SubmitResult struct {
	Tx             *domain.WagerTransaction
	Replay         bool // operação já existia: nada foi reaplicado
	InboxDuplicate bool // mensagem SQS já tratada antes
}
