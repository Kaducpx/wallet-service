package app

import (
	"context"
	"errors"

	"wallet-service/internal/domain"
)

// Idempotência: mandar a mesma operação várias vezes tem o mesmo efeito de
// mandar uma vez. Se o provedor reenviar uma aposta por timeout, o segundo
// envio encontra a operação gravada e recebe a mesma resposta, sem debitar
// de novo. Fica tudo no banco, então vale entre instâncias e após restart.

// findExisting procura a operação:
//   - mesma chave e mesmo conteúdo: replay;
//   - mesma chave e conteúdo diferente: ErrIdempotencyConflict;
//   - mesmo id externo com outra chave: ErrExternalIDConflict.
//
// Devolve (nil, nil) se a operação é nova.
func (s *WagerService) findExisting(ctx context.Context, tx Tx, op WagerOperation, key string) (*domain.WagerTransaction, error) {
	existing, err := tx.FindByIdempotencyKey(ctx, op.ProviderID, key)
	if err == nil {
		if err := CheckReplay(existing, op.PayloadHash); err != nil {
			return nil, err
		}
		return existing, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}

	// Impede reaplicar a mesma operação do provedor trocando a chave.
	byExt, err := tx.FindByExternalID(ctx, op.ProviderID, op.ExternalTransactionID)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// Em READ COMMITTED cada SELECT vê um snapshot próprio. Uma requisição
	// com a MESMA chave pode ter commitado entre a busca acima e esta. Por
	// isso compara a chave antes de chamar de conflito.
	if byExt.External() != nil && byExt.External().IdempotencyKey == key {
		if err := CheckReplay(byExt, op.PayloadHash); err != nil {
			return nil, err
		}
		return byExt, nil
	}
	return nil, ErrExternalIDConflict
}

// CheckReplay compara os hashes: iguais = mesma operação.
func CheckReplay(existing *domain.WagerTransaction, payloadHash string) error {
	ext := existing.External()
	if ext == nil || ext.PayloadHash != payloadHash {
		return ErrIdempotencyConflict
	}
	return nil
}

// completeInbox grava a mensagem SQS na inbox no MESMO commit do resto.
// Gravando antes, uma falha deixaria a mensagem marcada sem ter sido
// processada; gravando depois, uma queda no meio faria a reentrega processar
// de novo.
//
// Se prior tiver erro, só repassa (a transação vai ser desfeita mesmo).
func (s *WagerService) completeInbox(ctx context.Context, tx Tx, cmd SubmitCommand, t *domain.WagerTransaction, prior error) error {
	if prior != nil || cmd.Inbox == nil {
		return prior
	}
	rec := *cmd.Inbox
	if t != nil {
		id := t.ID()
		rec.TransactionID = &id
	}
	rec.CompletedAt = s.now()
	inserted, err := tx.InsertInbox(ctx, rec)
	if err != nil || inserted {
		return err
	}
	// Outra entrega da mesma mensagem gravou primeiro. Confere o conteúdo.
	existing, err := tx.FindInbox(ctx, rec.Consumer, rec.MessageID)
	if err != nil {
		return err
	}
	if existing.PayloadHash != rec.PayloadHash {
		return ErrInboxPayloadMismatch
	}
	return nil
}
