// Uso:
//
//	wallet-service                  sobe a API, o consumidor SQS e os workers
//	wallet-service migrate up       aplica as migrations
//	wallet-service migrate down N   desfaz N migrations (sem N: todas)
//	wallet-service migrate version  mostra a versão atual
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"go.uber.org/fx"

	"wallet-service/internal/adapters/postgres"
	"wallet-service/internal/bootstrap"
	"wallet-service/internal/platform/config"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		if err := runMigrate(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "migrate:", err)
			os.Exit(1)
		}
		return
	}
	if err := serve(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// serve sobe tudo e espera SIGINT/SIGTERM para desligar (prazo de
// SHUTDOWN_TIMEOUT).
func serve() error {
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	app := fx.New(bootstrap.Options(bootstrap.ConfigValue(cfg)), bootstrap.StopTimeout(cfg))
	if err := app.Err(); err != nil {
		return err
	}

	startCtx, cancel := context.WithTimeout(context.Background(), app.StartTimeout())
	defer cancel()
	if err := app.Start(startCtx); err != nil {
		return fmt.Errorf("start: %w", err)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	stopCtx, cancelStop := context.WithTimeout(context.Background(), app.StopTimeout())
	defer cancelStop()
	return app.Stop(stopCtx)
}

// runMigrate usa a conexão do dono do schema (MIGRATIONS_DATABASE_URL).
func runMigrate(args []string) error {
	url := os.Getenv("MIGRATIONS_DATABASE_URL")
	if url == "" {
		url = os.Getenv("DATABASE_URL")
	}
	if url == "" {
		return fmt.Errorf("MIGRATIONS_DATABASE_URL is required")
	}
	m, err := postgres.NewMigrator(url)
	if err != nil {
		return err
	}
	defer m.Close()

	cmd := "up"
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "up":
		if err := m.Up(); err != nil {
			return err
		}
	case "down":
		steps := 0
		if len(args) > 1 {
			if steps, err = strconv.Atoi(args[1]); err != nil {
				return fmt.Errorf("invalid steps %q", args[1])
			}
		}
		if err := m.Down(steps); err != nil {
			return err
		}
	case "version":
	default:
		return fmt.Errorf("unknown command %q (use up, down [N] or version)", cmd)
	}
	v, dirty, err := m.Version()
	if err != nil {
		return err
	}
	fmt.Printf("schema version: %d (dirty=%t)\n", v, dirty)
	return nil
}
