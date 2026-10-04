package postgres

import (
	"errors"
	"fmt"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5" // driver pgx5://
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"wallet-service/migrations"
)

// Migrator aplica e reverte as migrations embutidas no binário.
type Migrator struct {
	m *migrate.Migrate
}

// NewMigrator usa a conexão do dono do schema, não a da aplicação.
func NewMigrator(databaseURL string) (*Migrator, error) {
	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return nil, err
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, toMigrateURL(databaseURL))
	if err != nil {
		return nil, fmt.Errorf("open migrator: %w", err)
	}
	return &Migrator{m: m}, nil
}

// Up aplica todas as migrations pendentes.
func (m *Migrator) Up() error { return ignoreNoChange(m.m.Up()) }

// Down reverte "steps" migrations (steps <= 0 reverte todas).
func (m *Migrator) Down(steps int) error {
	if steps <= 0 {
		return ignoreNoChange(m.m.Down())
	}
	return ignoreNoChange(m.m.Steps(-steps))
}

// Version devolve a versão atual e se ficou "dirty" (migration quebrou no meio).
func (m *Migrator) Version() (uint, bool, error) {
	v, dirty, err := m.m.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		return 0, false, nil
	}
	return v, dirty, err
}

func (m *Migrator) Close() error {
	srcErr, dbErr := m.m.Close()
	return errors.Join(srcErr, dbErr)
}

// "Nada para aplicar" não é erro.
func ignoreNoChange(err error) error {
	if errors.Is(err, migrate.ErrNoChange) {
		return nil
	}
	return err
}

// toMigrateURL troca postgres:// por pgx5://, que é o que o driver espera.
func toMigrateURL(u string) string {
	for _, p := range []string{"postgres://", "postgresql://"} {
		if strings.HasPrefix(u, p) {
			return "pgx5://" + strings.TrimPrefix(u, p)
		}
	}
	return u
}
