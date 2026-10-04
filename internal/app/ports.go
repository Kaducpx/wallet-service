package app

import (
	"context"
	"time"

	"github.com/google/uuid"

	"wallet-service/internal/domain"
)

// Interfaces que a aplicação usa e os adaptadores implementam. Aqui não tem
// SQL: a aplicação diz o que precisa ("trave a carteira", "grave o
// lançamento") e o pacote postgres diz como.

// TxRunner abre a transação SQL. Tudo que roda dentro de fn usa a mesma
// transação: saldo, ledger, inbox e outbox são confirmados juntos ou não são.
// Retornar nil faz COMMIT; retornar erro faz ROLLBACK.
type TxRunner interface {
	// InTx: READ COMMITTED, com lock explícito por carteira.
	InTx(ctx context.Context, fn func(ctx context.Context, tx Tx) error) error
	// ReadOnly: REPEATABLE READ, só leitura. Todas as consultas veem o mesmo
	// momento (usado na reconciliação).
	ReadOnly(ctx context.Context, fn func(ctx context.Context, tx Tx) error) error
}

// TxMeta guarda dados de rastreamento que não fazem parte do domínio.
type TxMeta struct {
	CorrelationID string
}

// Tx é o que dá para fazer no banco dentro de uma transação.
type Tx interface {
	// Carteiras.
	InsertWallet(ctx context.Context, w *domain.Wallet) error
	GetWallet(ctx context.Context, id uuid.UUID) (*domain.Wallet, error)
	// LockWallet trava a carteira (FOR UPDATE) até o fim da transação.
	LockWallet(ctx context.Context, id uuid.UUID) (*domain.Wallet, error)
	// UpdateWalletBalance só grava se a versão no banco for a esperada; senão
	// devolve ErrConcurrentUpdate.
	UpdateWalletBalance(ctx context.Context, w *domain.Wallet, expectedVersion int64) error

	// Transações de aposta.
	InsertTransaction(ctx context.Context, t *domain.WagerTransaction, meta TxMeta) error
	// UpdateTransaction grava só as colunas que podem mudar.
	UpdateTransaction(ctx context.Context, t *domain.WagerTransaction) error
	GetTransaction(ctx context.Context, id uuid.UUID) (*domain.WagerTransaction, error)
	// LockTransaction lê com FOR UPDATE (usado na retomada de pendências).
	LockTransaction(ctx context.Context, id uuid.UUID) (*domain.WagerTransaction, TxMeta, error)
	// Buscas sempre dentro do provedor.
	FindByIdempotencyKey(ctx context.Context, providerID, key string) (*domain.WagerTransaction, error)
	FindByExternalID(ctx context.Context, providerID, externalID string) (*domain.WagerTransaction, error)
	// HasProcessedReversal: a transação já foi revertida?
	HasProcessedReversal(ctx context.Context, referenceTxID uuid.UUID) (bool, error)
	// WakePendingReferencing adianta quem está esperando por esta operação.
	WakePendingReferencing(ctx context.Context, providerID, externalID string) error

	// Ledger: só insere e lê, nunca altera.
	InsertLedgerEntry(ctx context.Context, e domain.LedgerEntry) error
	// ListLedger devolve até limit lançamentos depois de afterVersion.
	ListLedger(ctx context.Context, walletID uuid.UUID, afterVersion int64, limit int) ([]domain.LedgerEntry, error)
	// LedgerTotals soma créditos e débitos.
	LedgerTotals(ctx context.Context, walletID uuid.UUID) (LedgerTotals, error)

	// Inbox: mensagens SQS já tratadas.
	FindInbox(ctx context.Context, consumer, messageID string) (*InboxRecord, error)
	// InsertInbox devolve false se a mensagem já estava registrada.
	InsertInbox(ctx context.Context, rec InboxRecord) (bool, error)

	// Outbox: eventos gravados junto com o resto.
	InsertOutbox(ctx context.Context, msgs []OutboxMessage) error
}

// LedgerTotals é a soma do ledger de uma carteira.
type LedgerTotals struct {
	CreditUnits int64
	DebitUnits  int64
	Entries     int64
}

// InboxRecord marca que uma mensagem já foi tratada. Se a mesma mensagem
// chegar de novo, este registro mostra que não precisa reprocessar.
type InboxRecord struct {
	Consumer      string     // nome do consumidor (permite vários consumidores da mesma fila)
	MessageID     string     // messageId do envelope (não o id interno do SQS)
	PayloadHash   string     // detecta mesmo messageId com conteúdo diferente
	TransactionID *uuid.UUID // transação resultante, para auditoria
	ReceivedAt    time.Time
	CompletedAt   time.Time
}

// OutboxMessage é um evento esperando para ser publicado.
//
// Em vez de publicar direto na fila (o que podia acontecer antes do commit,
// ou se perder numa queda logo depois), o evento é gravado numa tabela na
// mesma transação do negócio. Um worker lê a tabela e publica.
type OutboxMessage struct {
	ID            uuid.UUID // eventId (estável: republicações usam o mesmo)
	AggregateType string    // "wallet"
	AggregateID   uuid.UUID // walletId
	EventType     string
	EventVersion  int
	Payload       []byte // envelope JSON pronto para envio
	CorrelationID string
	OccurredAt    time.Time
	Attempts      int // quantas vezes o publicador já tentou enviar
}
