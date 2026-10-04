package httpapi

import (
	"log/slog"

	"wallet-service/internal/app"
)

// Limite de 64 KiB por corpo. Uma operação real tem poucas centenas de bytes.
const maxBodyBytes = 64 << 10

// Handlers são finos: leem a requisição, chamam o caso de uso e respondem.
type Handlers struct {
	wallets *app.WalletService
	wager   *app.WagerService
	log     *slog.Logger
}

func NewHandlers(wallets *app.WalletService, wager *app.WagerService, log *slog.Logger) *Handlers {
	return &Handlers{wallets: wallets, wager: wager, log: log}
}
