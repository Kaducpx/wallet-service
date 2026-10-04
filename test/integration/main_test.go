//go:build integration

// Package integration contém os testes de integração. Eles usam containers
// reais (PostgreSQL, Keycloak e LocalStack do docker-compose) — nada de mocks
// de infraestrutura.
//
//	docker compose up -d postgres keycloak localstack
//	go test -tags integration -count=1 ./test/integration/...
//
// Cada execução recria o banco wallet_it (separado do banco da aplicação),
// aplica as migrations e cria filas SQS exclusivas por teste.
package integration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"wallet-service/internal/adapters/postgres"
)

// Endereços da infraestrutura (sobrescrevíveis por variável de ambiente para
// rodar os testes dentro da rede do compose).
var (
	pgHost      = envOr("IT_PG_HOST", "localhost:55432")
	ownerUser   = envOr("IT_PG_OWNER", "wallet_owner:wallet_owner_local")
	appUser     = envOr("IT_PG_APP", "wallet_app:wallet_app_local")
	testDBName  = envOr("IT_DB_NAME", "wallet_it")
	keycloakURL = envOr("IT_KEYCLOAK_URL", "http://localhost:8080")
	oidcIssuer  = envOr("IT_OIDC_ISSUER", "http://localhost:8080/realms/wallet")
	awsEndpoint = envOr("IT_AWS_ENDPOINT", "http://localhost:4566")
)

var (
	ownerDBURL string        // dono do schema no banco de teste
	appDBURL   string        // papel restrito da aplicação no banco de teste
	db         *pgxpool.Pool // conexão de asserções (dono)
	binaryPath string        // binário compilado para testes multi-processo
)

// envOr lê uma variável de ambiente, com valor padrão.
func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// TestMain prepara a infraestrutura uma vez antes de todos os testes.
func TestMain(m *testing.M) {
	os.Setenv("AWS_ACCESS_KEY_ID", envOr("AWS_ACCESS_KEY_ID", "test"))
	os.Setenv("AWS_SECRET_ACCESS_KEY", envOr("AWS_SECRET_ACCESS_KEY", "test"))
	os.Setenv("AWS_REGION", envOr("AWS_REGION", "us-east-1"))

	code, err := setup()
	if err != nil {
		fmt.Fprintln(os.Stderr, "integration setup failed:", err)
		os.Exit(1)
	}
	if code == 0 {
		code = m.Run()
	}
	if db != nil {
		db.Close()
	}
	os.Exit(code)
}

// setup recria o banco de teste, aplica as migrations e compila o binário
// usado pelos testes multi-processo.
func setup() (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	ownerDBURL = fmt.Sprintf("postgres://%s@%s/%s?sslmode=disable", ownerUser, pgHost, testDBName)
	appDBURL = fmt.Sprintf("postgres://%s@%s/%s?sslmode=disable", appUser, pgHost, testDBName)

	// Recria o banco de teste a partir do zero.
	admin, err := pgx.Connect(ctx, fmt.Sprintf("postgres://%s@%s/postgres?sslmode=disable", ownerUser, pgHost))
	if err != nil {
		return 1, fmt.Errorf("connect postgres (is docker compose up?): %w", err)
	}
	if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+testDBName+" WITH (FORCE)"); err != nil {
		return 1, err
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+testDBName); err != nil {
		return 1, err
	}
	admin.Close(ctx)

	mig, err := postgres.NewMigrator(ownerDBURL)
	if err != nil {
		return 1, err
	}
	if err := mig.Up(); err != nil {
		return 1, fmt.Errorf("migrate up: %w", err)
	}
	mig.Close()

	db, err = pgxpool.New(ctx, ownerDBURL)
	if err != nil {
		return 1, err
	}

	// Binário para os testes com processos independentes.
	dir, err := os.MkdirTemp("", "wallet-it-bin")
	if err != nil {
		return 1, err
	}
	binaryPath = filepath.Join(dir, "wallet-service")
	if runtime.GOOS == "windows" {
		binaryPath += ".exe"
	}
	build := exec.Command("go", "build", "-o", binaryPath, "../../cmd/wallet-service")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		return 1, fmt.Errorf("build binary: %w", err)
	}
	return 0, nil
}
