package postgres

import (
	"context"

	"wallet-service/internal/app"
)

// FindInbox: ErrNotFound se a mensagem é nova.
func (r *txRepo) FindInbox(ctx context.Context, consumer, messageID string) (*app.InboxRecord, error) {
	var rec app.InboxRecord
	err := r.tx.QueryRow(ctx, `
		SELECT consumer_name, message_id, payload_hash, transaction_id, received_at, completed_at
		  FROM inbox_messages WHERE consumer_name = $1 AND message_id = $2`, consumer, messageID).
		Scan(&rec.Consumer, &rec.MessageID, &rec.PayloadHash, &rec.TransactionID, &rec.ReceivedAt, &rec.CompletedAt)
	if err != nil {
		return nil, wrap("find inbox", err)
	}
	return &rec, nil
}

// InsertInbox devolve false se a mensagem já estava lá. Se duas entregas
// correrem juntas, a segunda espera a primeira terminar e recebe false.
func (r *txRepo) InsertInbox(ctx context.Context, rec app.InboxRecord) (bool, error) {
	tag, err := r.tx.Exec(ctx, `
		INSERT INTO inbox_messages (consumer_name, message_id, payload_hash, transaction_id, received_at, completed_at)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (consumer_name, message_id) DO NOTHING`,
		rec.Consumer, rec.MessageID, rec.PayloadHash, rec.TransactionID, rec.ReceivedAt, rec.CompletedAt)
	if err != nil {
		return false, wrap("insert inbox", err)
	}
	return tag.RowsAffected() == 1, nil
}
