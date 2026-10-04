package domain

// Status da transação.
//
//	PENDING ──────────────┬──> PROCESSED  (final)
//	   │                  ├──> REJECTED   (final)
//	   │                  └──> FAILED     (final)
//	   └──> PENDING_REFERENCE ─┬──> PROCESSED
//	          (retry/backoff)  ├──> REJECTED (ex.: REFERENCE_NOT_FOUND)
//	                           └──> FAILED
//
// PENDING só existe em memória: a operação é decidida e gravada no mesmo
// commit. O único estado não final que vai para o banco é PENDING_REFERENCE,
// e qualquer instância pode retomá-lo.
type Status string

const (
	StatusPending          Status = "PENDING"           // sendo decidida
	StatusPendingReference Status = "PENDING_REFERENCE" // esperando a referência chegar
	StatusProcessed        Status = "PROCESSED"         // deu certo
	StatusRejected         Status = "REJECTED"          // recusada por regra de negócio
	StatusFailed           Status = "FAILED"            // falha permanente, para auditoria
)

// IsTerminal: estado final não muda mais; reenvios só leem o resultado.
func (s Status) IsTerminal() bool {
	return s == StatusProcessed || s == StatusRejected || s == StatusFailed
}

// allowedTransitions lista, para cada estado, para onde ele pode ir. Estados
// finais não aparecem aqui, então não têm saída.
var allowedTransitions = map[Status][]Status{
	StatusPending:          {StatusPendingReference, StatusProcessed, StatusRejected, StatusFailed},
	StatusPendingReference: {StatusProcessed, StatusRejected, StatusFailed},
}

func canTransition(from, to Status) bool {
	for _, s := range allowedTransitions[from] {
		if s == to {
			return true
		}
	}
	return false
}
