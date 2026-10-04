package app

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"wallet-service/internal/domain"
	"wallet-service/internal/domain/money"
)

// WalletService abre, consulta e reconcilia carteiras. Só o serviço interno
// (papel wallet-operator) tem acesso.
type WalletService struct {
	tx      TxRunner
	metrics Metrics
	log     *slog.Logger
	now     func() time.Time
}

func NewWalletService(tx TxRunner, metrics Metrics, log *slog.Logger) *WalletService {
	return &WalletService{tx: tx, metrics: metrics, log: log, now: Now}
}

// OpenWalletCommand são os dados para abrir uma carteira.
type OpenWalletCommand struct {
	PlayerID       uuid.UUID
	InitialBalance money.Money // pode ser zero; define também a moeda da carteira
	CorrelationID  string
}

// Open cria a carteira e, se houver saldo inicial, o OPENING, o lançamento
// e os eventos, tudo no mesmo commit.
func (s *WalletService) Open(ctx context.Context, cmd OpenWalletCommand) (*domain.Wallet, error) {
	now := s.now()
	opened, err := domain.OpenWallet(domain.OpenWalletParams{
		WalletID: newID(), PlayerID: cmd.PlayerID, InitialBalance: cmd.InitialBalance,
		OpeningTransactionID: newID(), OpeningEntryID: newID(), Now: now,
	})
	if err != nil {
		return nil, err
	}
	outbox, err := buildOutbox(opened.Events, cmd.CorrelationID, "", now)
	if err != nil {
		return nil, err
	}

	err = s.tx.InTx(ctx, func(ctx context.Context, tx Tx) error {
		if err := tx.InsertWallet(ctx, opened.Wallet); err != nil {
			// Índice único (player_id, currency): já existe carteira nessa moeda.
			if errors.Is(err, ErrUniqueViolation) {
				return ErrWalletAlreadyExists
			}
			return err
		}
		if opened.Opening == nil {
			return nil // saldo zero: sem OPENING, ledger ou eventos financeiros
		}
		// A ordem importa por causa das FKs: transação, lançamento, eventos.
		if err := tx.InsertTransaction(ctx, opened.Opening, TxMeta{CorrelationID: cmd.CorrelationID}); err != nil {
			return err
		}
		if err := tx.InsertLedgerEntry(ctx, *opened.Entry); err != nil {
			return err
		}
		return tx.InsertOutbox(ctx, outbox)
	})
	if err != nil {
		return nil, err
	}
	s.log.InfoContext(ctx, "wallet opened",
		slog.String("walletId", opened.Wallet.ID().String()),
		slog.String("correlationId", cmd.CorrelationID))
	return opened.Wallet, nil
}

// Get busca a carteira pelo id.
func (s *WalletService) Get(ctx context.Context, id uuid.UUID) (*domain.Wallet, error) {
	var w *domain.Wallet
	err := s.tx.ReadOnly(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		w, err = tx.GetWallet(ctx, id)
		return err
	})
	if errors.Is(err, ErrNotFound) {
		return nil, ErrWalletNotFound
	}
	return w, err
}

// LedgerPage é uma página do extrato. NextAfterVersion == 0 indica fim.
type LedgerPage struct {
	Entries          []domain.LedgerEntry
	NextAfterVersion int64
}

// Ledger pagina o extrato por walletVersion (única por carteira). Com cursor
// em vez de OFFSET, nada é pulado ou repetido se entrar lançamento novo
// durante a paginação.
func (s *WalletService) Ledger(ctx context.Context, walletID uuid.UUID, afterVersion int64, limit int) (LedgerPage, error) {
	var page LedgerPage
	err := s.tx.ReadOnly(ctx, func(ctx context.Context, tx Tx) error {
		if _, err := tx.GetWallet(ctx, walletID); err != nil {
			return err
		}
		// Pede um a mais para saber se existe próxima página.
		entries, err := tx.ListLedger(ctx, walletID, afterVersion, limit+1)
		if err != nil {
			return err
		}
		if len(entries) > limit {
			entries = entries[:limit]
			page.NextAfterVersion = entries[len(entries)-1].WalletVersion()
		}
		page.Entries = entries
		return nil
	})
	if errors.Is(err, ErrNotFound) {
		return LedgerPage{}, ErrWalletNotFound
	}
	return page, err
}
