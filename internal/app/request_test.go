package app

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"wallet-service/internal/domain"
	"wallet-service/internal/domain/money"
)

// validRaw devolve uma requisição válida (a do exemplo do enunciado), usada
// como ponto de partida: cada teste altera um campo e verifica o erro.
func validRaw() RawWagerRequest {
	return RawWagerRequest{
		ProviderID: "provider-a", ExternalTransactionID: "transaction-123",
		PlayerID: "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1", WalletID: "0192f291-27dd-7d3f-8071-5f8685deef37",
		RoundID: "round-987", GameID: "fortune-chimp", Kind: "BET",
		Money: &RawMoney{Amount: "25.00", Currency: "BRL"},
	}
}

// validationCode extrai o código de um *ValidationError ("" se não for um).
func validationCode(err error) string {
	var ve *ValidationError
	if errors.As(err, &ve) {
		return ve.Code
	}
	return ""
}

// Uma requisição válida é aceita e ganha o hash SHA-256 (64 caracteres hex).
func TestParseWagerRequestValid(t *testing.T) {
	op, err := ParseWagerRequest(validRaw())
	if err != nil {
		t.Fatal(err)
	}
	if op.Kind != domain.KindBet || op.Money.Amount() != "25.00" || len(op.PayloadHash) != 64 {
		t.Fatalf("unexpected op: %+v", op)
	}
}

// Cada entrada inválida devolve o código de erro esperado (tabela de casos).
func TestParseWagerRequestRejections(t *testing.T) {
	cases := map[string]struct {
		mutate func(*RawWagerRequest)
		code   string
	}{
		"opening":         {func(r *RawWagerRequest) { r.Kind = "OPENING" }, CodeOpeningNotAllowed},
		"unknown kind":    {func(r *RawWagerRequest) { r.Kind = "DEPOSIT" }, CodeInvalidKind},
		"float amount":    {func(r *RawWagerRequest) { r.Money.Amount = "25.5" }, CodeInvalidMoney},
		"scientific":      {func(r *RawWagerRequest) { r.Money.Amount = "2.5e1" }, CodeInvalidMoney},
		"negative":        {func(r *RawWagerRequest) { r.Money.Amount = "-25.00" }, CodeInvalidMoney},
		"NaN":             {func(r *RawWagerRequest) { r.Money.Amount = "NaN" }, CodeInvalidMoney},
		"empty amount":    {func(r *RawWagerRequest) { r.Money.Amount = "" }, CodeInvalidMoney},
		"no money":        {func(r *RawWagerRequest) { r.Money = nil }, CodeInvalidMoney},
		"currency":        {func(r *RawWagerRequest) { r.Money.Currency = "brl" }, CodeUnsupportedCurrency},
		"bet zero":        {func(r *RawWagerRequest) { r.Money.Amount = "0.00" }, CodeInvalidAmountForKind},
		"loss nonzero":    {func(r *RawWagerRequest) { r.Kind = "LOSS" }, CodeInvalidAmountForKind},
		"refund no ref":   {func(r *RawWagerRequest) { r.Kind = "REFUND" }, CodeMissingReference},
		"rollback no ref": {func(r *RawWagerRequest) { r.Kind = "ROLLBACK" }, CodeMissingReference},
		"bet with ref":    {func(r *RawWagerRequest) { r.ReferenceExternalTransactionID = "x" }, CodeInvalidField},
		"self reference": {func(r *RawWagerRequest) {
			r.Kind = "REFUND"
			r.ReferenceExternalTransactionID = r.ExternalTransactionID
		}, CodeInvalidField},
		"bad player":       {func(r *RawWagerRequest) { r.PlayerID = "not-a-uuid" }, CodeInvalidField},
		"nil wallet":       {func(r *RawWagerRequest) { r.WalletID = uuid.Nil.String() }, CodeInvalidField},
		"missing provider": {func(r *RawWagerRequest) { r.ProviderID = "" }, CodeInvalidField},
		"spaces in id":     {func(r *RawWagerRequest) { r.RoundID = " round" }, CodeInvalidField},
		"long id":          {func(r *RawWagerRequest) { r.GameID = strings.Repeat("g", 129) }, CodeInvalidField},
	}
	for name, c := range cases {
		r := validRaw()
		m := *r.Money
		r.Money = &m
		c.mutate(&r)
		if _, err := ParseWagerRequest(r); validationCode(err) != c.code {
			t.Errorf("%s: expected %s, got %v", name, c.code, err)
		}
	}
}

// LOSS com 0.00 é aceito (a única operação que aceita valor zero).
func TestZeroPolicyAccepted(t *testing.T) {
	r := validRaw()
	r.Kind = "LOSS"
	r.Money = &RawMoney{Amount: "0.00", Currency: "BRL"}
	if _, err := ParseWagerRequest(r); err != nil {
		t.Fatalf("LOSS 0.00 must be accepted: %v", err)
	}
}

// O hash não muda com detalhes irrelevantes (maiúsculas no UUID, canal de
// entrada, chave de idempotência) e muda com qualquer campo de negócio.
func TestPayloadHashIsCanonical(t *testing.T) {
	a, _ := ParseWagerRequest(validRaw())

	// UUID em maiúsculas é normalizado: mesmo hash.
	upper := validRaw()
	upper.PlayerID = strings.ToUpper(upper.PlayerID)
	b, err := ParseWagerRequest(upper)
	if err != nil {
		t.Fatal(err)
	}
	if a.PayloadHash != b.PayloadHash {
		t.Fatal("hash must not depend on UUID letter case")
	}

	// A chave de idempotência e metadados de transporte não entram no hash:
	// o mesmo corpo vindo do SQS (dentro de data, com idempotencyKey) gera o
	// mesmo hash do HTTP.
	sqsData := `{"providerId":"provider-a","externalTransactionId":"transaction-123","idempotencyKey":"provider-a:transaction-123",
		"playerId":"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1","walletId":"0192f291-27dd-7d3f-8071-5f8685deef37",
		"roundId":"round-987","gameId":"fortune-chimp","kind":"BET","money":{"currency":"BRL","amount":"25.00"}}`
	var fromSQS struct {
		RawWagerRequest
		IdempotencyKey string `json:"idempotencyKey"`
	}
	if err := json.Unmarshal([]byte(sqsData), &fromSQS); err != nil {
		t.Fatal(err)
	}
	c, err := ParseWagerRequest(fromSQS.RawWagerRequest)
	if err != nil {
		t.Fatal(err)
	}
	if a.PayloadHash != c.PayloadHash {
		t.Fatal("HTTP and SQS must produce the same hash")
	}

	// Qualquer campo de negócio diferente muda o hash.
	for name, mutate := range map[string]func(*RawWagerRequest){
		"amount":   func(r *RawWagerRequest) { r.Money = &RawMoney{Amount: "25.01", Currency: "BRL"} },
		"currency": func(r *RawWagerRequest) { r.Money = &RawMoney{Amount: "25.00", Currency: "USD"} },
		"round":    func(r *RawWagerRequest) { r.RoundID = "round-988" },
		"game":     func(r *RawWagerRequest) { r.GameID = "other" },
		"kind":     func(r *RawWagerRequest) { r.Kind = "WIN" },
	} {
		r := validRaw()
		mutate(&r)
		d, err := ParseWagerRequest(r)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if d.PayloadHash == a.PayloadHash {
			t.Errorf("%s: hash must change", name)
		}
	}
}

// Mesma chave com conteúdo diferente é conflito; conteúdo igual é replay.
func TestCheckReplayDetectsPayloadConflict(t *testing.T) {
	op, _ := ParseWagerRequest(validRaw())
	brl := money.MustCurrency("BRL")
	bal, _ := money.FromUnits(97500, brl)
	tx, err := domain.RehydrateTransaction(domain.TransactionSnapshot{
		ID: uuid.New(), Origin: domain.OriginExternal, Kind: domain.KindBet, Status: domain.StatusProcessed,
		WalletID: op.WalletID, PlayerID: op.PlayerID, Money: op.Money,
		External: &domain.ExternalInfo{
			ProviderID: op.ProviderID, ExternalTransactionID: op.ExternalTransactionID,
			IdempotencyKey: "provider-a:transaction-123", PayloadHash: op.PayloadHash,
			RoundID: op.RoundID, GameID: op.GameID,
		},
		ResultBalance: &bal, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckReplay(tx, op.PayloadHash); err != nil {
		t.Fatalf("same payload must replay: %v", err)
	}
	other := validRaw()
	other.Money = &RawMoney{Amount: "30.00", Currency: "BRL"}
	op2, _ := ParseWagerRequest(other)
	if err := CheckReplay(tx, op2.PayloadHash); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("different payload must conflict: %v", err)
	}
}

// A chave de idempotência é obrigatória e não aceita espaços.
func TestValidateIdempotencyKey(t *testing.T) {
	if validationCode(ValidateIdempotencyKey("")) != CodeMissingIdempotency {
		t.Error("empty key must be rejected")
	}
	if ValidateIdempotencyKey("provider-a:transaction-123") != nil {
		t.Error("valid key rejected")
	}
	if ValidateIdempotencyKey("with space") == nil {
		t.Error("spaces must be rejected")
	}
}
