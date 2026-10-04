package app

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"wallet-service/internal/domain"
)

// Get devolve uma transação pelo id interno.
func (s *WagerService) Get(ctx context.Context, id uuid.UUID) (*domain.WagerTransaction, error) {
	var t *domain.WagerTransaction
	err := s.tx.ReadOnly(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		t, err = tx.GetTransaction(ctx, id)
		return err
	})
	if errors.Is(err, ErrNotFound) {
		return nil, ErrTransactionNotFound
	}
	return t, err
}

// GetByExternalID devolve a transação de um provedor pelo id externo.
func (s *WagerService) GetByExternalID(ctx context.Context, providerID, externalID string) (*domain.WagerTransaction, error) {
	var t *domain.WagerTransaction
	err := s.tx.ReadOnly(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		t, err = tx.FindByExternalID(ctx, providerID, externalID)
		return err
	})
	if errors.Is(err, ErrNotFound) {
		return nil, ErrTransactionNotFound
	}
	return t, err
}
