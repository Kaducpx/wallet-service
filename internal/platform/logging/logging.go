// Package logging configura os logs em JSON.
//
// Logs levam só ids (correlationId, messageId, transactionId, walletId,
// providerId) e códigos. Nunca token, segredo ou payload financeiro completo.
package logging

import (
	"io"
	"log/slog"
	"os"
	"strings"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"wallet-service/internal/platform/config"
)

var Module = fx.Module("logging",
	fx.Provide(NewLogger),
)

// NewLogger põe o id da instância em todas as linhas.
func NewLogger(cfg config.Config) *slog.Logger {
	return New(os.Stdout, cfg.LogLevel).With(slog.String("instanceId", cfg.InstanceID))
}

func New(w io.Writer, level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: lvl}))
}

// FxLogger manda os logs do próprio Fx para o mesmo lugar.
func FxLogger(log *slog.Logger) fxevent.Logger {
	l := &fxevent.SlogLogger{Logger: log.With(slog.String("component", "fx"))}
	l.UseLogLevel(slog.LevelDebug)
	return l
}
