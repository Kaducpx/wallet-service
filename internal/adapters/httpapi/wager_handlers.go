package httpapi

import (
	"net/http"

	"wallet-service/internal/app"
	"wallet-service/internal/auth"
	"wallet-service/internal/domain"
)

// submitWager: POST /wagering/transactions. O provedor só envia em nome
// próprio, e isso é checado antes de tocar no banco.
func (h *Handlers) submitWager(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())

	key := r.Header.Get("Idempotency-Key")
	if err := app.ValidateIdempotencyKey(key); err != nil {
		writeError(w, r, h.log, err)
		return
	}
	var raw app.RawWagerRequest
	if err := decodeStrict(r, &raw); err != nil {
		writeError(w, r, h.log, err)
		return
	}
	// O providerId do corpo tem que ser o do token. Ninguém age em nome de
	// outro provedor, nem para fazer replay.
	if raw.ProviderID != p.ProviderID {
		writeError(w, r, h.log, errProviderMismatch)
		return
	}
	op, err := app.ParseWagerRequest(raw)
	if err != nil {
		writeError(w, r, h.log, err)
		return
	}
	res, err := h.wager.Submit(r.Context(), app.SubmitCommand{
		Op: op, IdempotencyKey: key, CorrelationID: correlationID(r), Channel: app.ChannelHTTP,
	})
	if err != nil {
		writeError(w, r, h.log, err)
		return
	}
	writeJSON(w, submitStatus(res), submitResponse{
		TransactionID: res.Tx.ID(), Status: res.Tx.Status(), Balance: res.Tx.ResultBalance(),
		FailureCode: string(res.Tx.FailureCode()), IdempotentReplay: res.Replay,
	})
}

// submitStatus escolhe o status HTTP pelo estado gravado:
//
//	PROCESSED          201 (novo) / 200 (replay)
//	PENDING_REFERENCE  202
//	REJECTED           422
//	FAILED             500
func submitStatus(res app.SubmitResult) int {
	switch res.Tx.Status() {
	case domain.StatusProcessed:
		if res.Replay {
			return http.StatusOK
		}
		return http.StatusCreated
	case domain.StatusPendingReference:
		return http.StatusAccepted
	case domain.StatusRejected:
		return http.StatusUnprocessableEntity
	default:
		return http.StatusInternalServerError
	}
}

// getTransaction: operador vê qualquer uma; provedor só as dele. Para os
// outros devolve 404, sem revelar que o id existe.
func (h *Handlers) getTransaction(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, h.log, "transactionId")
	if !ok {
		return
	}
	p, _ := auth.FromContext(r.Context())
	t, err := h.wager.Get(r.Context(), id)
	if err != nil {
		writeError(w, r, h.log, err)
		return
	}
	if !p.IsOperator() && (t.ProviderID() == "" || !p.CanAccessProvider(t.ProviderID())) {
		writeError(w, r, h.log, app.ErrTransactionNotFound)
		return
	}
	writeJSON(w, http.StatusOK, toTransaction(t))
}

// getProviderTransaction busca pelo id externo. Provedor só consulta o
// próprio providerId (senão 403).
func (h *Handlers) getProviderTransaction(w http.ResponseWriter, r *http.Request) {
	providerID, extID := r.PathValue("providerId"), r.PathValue("externalTransactionId")
	p, _ := auth.FromContext(r.Context())
	if !p.CanAccessProvider(providerID) {
		writeError(w, r, h.log, errForbidden)
		return
	}
	t, err := h.wager.GetByExternalID(r.Context(), providerID, extID)
	if err != nil {
		writeError(w, r, h.log, err)
		return
	}
	writeJSON(w, http.StatusOK, toTransaction(t))
}
