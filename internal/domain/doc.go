// Package domain tem as regras de negócio: carteira, transações de aposta,
// ledger e eventos. Não depende de Fx, HTTP, SQS nem banco. O dinheiro fica
// no subpacote money.
//
// Por onde começar:
//
//	wallet.go             carteira: abertura, débito e crédito
//	ledger.go             lançamento do extrato (nunca muda)
//	kind.go, status.go    tipos de operação e estados
//	transaction.go        a transação de aposta
//	transaction_state.go  mudanças de estado
//	settlement.go         regras de BET, WIN, LOSS, REFUND e ROLLBACK
//	pending.go            espera pela referência (retry e prazo)
//	events.go             eventos publicados
//	failure_code.go       códigos de rejeição
//
// New* cria algo novo e aplica as regras. Rehydrate* só carrega do banco,
// sem gerar eventos nem refazer movimentações.
package domain
