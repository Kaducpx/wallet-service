// Package postgres implementa o acesso ao banco com pgx e SQL escrito à mão,
// para que transações, locks e constraints fiquem visíveis.
//
// Os métodos de txRepo rodam todos na transação aberta por Store.InTx.
// OutboxRepo e PendingRepo usam transações curtas próprias, porque servem
// aos workers.
package postgres
