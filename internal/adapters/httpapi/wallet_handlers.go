package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"wallet-service/internal/app"
	"wallet-service/internal/domain/money"
)

// Endpoints do serviço interno. O middleware já conferiu o papel.

// openWallet: POST /wallets. 201, ou 409 se já existir carteira na moeda.
func (h *Handlers) openWallet(w http.ResponseWriter, r *http.Request) {
	var req openWalletRequest
	if err := decodeStrict(r, &req); err != nil {
		writeError(w, r, h.log, err)
		return
	}
	playerID, err := uuid.Parse(req.PlayerID)
	if err != nil || playerID == uuid.Nil || len(req.PlayerID) != 36 {
		writeError(w, r, h.log, &app.ValidationError{Code: app.CodeInvalidField, Field: "playerId", Message: "playerId must be a UUID"})
		return
	}
	if req.InitialBalance == nil {
		writeError(w, r, h.log, &app.ValidationError{Code: app.CodeInvalidMoney, Field: "initialBalance", Message: "initialBalance is required"})
		return
	}
	initial, err := money.Parse(req.InitialBalance.Amount, req.InitialBalance.Currency)
	if err != nil {
		code := app.CodeInvalidMoney
		if errors.Is(err, money.ErrUnsupportedCurrency) {
			code = app.CodeUnsupportedCurrency
		}
		writeError(w, r, h.log, &app.ValidationError{Code: code, Field: "initialBalance", Message: err.Error()})
		return
	}
	wallet, err := h.wallets.Open(r.Context(), app.OpenWalletCommand{
		PlayerID: playerID, InitialBalance: initial, CorrelationID: correlationID(r),
	})
	if err != nil {
		writeError(w, r, h.log, err)
		return
	}
	writeJSON(w, http.StatusCreated, toWallet(wallet))
}

// getWallet: GET /wallets/{walletId}.
func (h *Handlers) getWallet(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, h.log, "walletId")
	if !ok {
		return
	}
	wallet, err := h.wallets.Get(r.Context(), id)
	if err != nil {
		writeError(w, r, h.log, err)
		return
	}
	writeJSON(w, http.StatusOK, toWallet(wallet))
}

// getLedger: GET /wallets/{walletId}/ledger?cursor=...&limit=50.
func (h *Handlers) getLedger(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, h.log, "walletId")
	if !ok {
		return
	}
	after, err := decodeCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		writeError(w, r, h.log, &app.ValidationError{Code: app.CodeInvalidField, Field: "cursor", Message: err.Error()})
		return
	}
	limit := 50
	if s := r.URL.Query().Get("limit"); s != "" {
		limit, err = strconv.Atoi(s)
		if err != nil || limit < 1 || limit > 200 {
			writeError(w, r, h.log, &app.ValidationError{Code: app.CodeInvalidField, Field: "limit", Message: "limit must be between 1 and 200"})
			return
		}
	}
	page, err := h.wallets.Ledger(r.Context(), id, after, limit)
	if err != nil {
		writeError(w, r, h.log, err)
		return
	}
	writeJSON(w, http.StatusOK, toLedgerPage(id, page))
}

// reconcile: POST /wallets/{walletId}/reconciliation. Só compara, não altera.
func (h *Handlers) reconcile(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, h.log, "walletId")
	if !ok {
		return
	}
	rec, err := h.wallets.Reconcile(r.Context(), id)
	if err != nil {
		writeError(w, r, h.log, err)
		return
	}
	writeJSON(w, http.StatusOK, reconciliationResponse{
		WalletID: rec.WalletID, StoredBalance: rec.StoredBalance, CalculatedBalance: rec.CalculatedBalance,
		Difference: rec.Difference, Consistent: rec.Consistent, CheckedEntries: rec.CheckedEntries,
	})
}
