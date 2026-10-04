// Package config lê a configuração das variáveis de ambiente. Configuração
// inválida impede a aplicação de subir.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config: cada campo vem de uma variável de ambiente (lista no README). Os
// valores padrão servem para rodar localmente.
type Config struct {
	InstanceID         string
	LogLevel           string
	HTTPAddr           string
	HTTPRequestTimeout time.Duration // prazo máximo de cada requisição HTTP
	ShutdownTimeout    time.Duration

	// Postgres. DatabaseURL usa o usuário restrito da aplicação;
	// MigrationsDatabaseURL usa o dono do schema (só para migrations).
	DatabaseURL           string
	MigrationsDatabaseURL string
	DBMaxConns            int32
	DBLockTimeout         time.Duration

	// OIDC / IdP.
	OIDCIssuer   string // valor esperado no claim iss
	OIDCJWKSURL  string // de onde buscar as chaves públicas
	OIDCAudience string // valor esperado no claim aud

	// AWS / SQS.
	AWSRegion             string
	AWSEndpointURL        string // LocalStack; vazio em AWS real
	SQSQueueName          string
	SQSDLQName            string
	SQSEventsQueueName    string
	SQSConsumerEnabled    bool
	SQSConsumerName       string // identidade do consumidor na inbox
	SQSConsumerWorkers    int
	SQSWaitSeconds        int32
	SQSMaxMessages        int32
	SQSVisibilityTimeout  int32 // segundos; deve coincidir com a fila
	SQSRetryBase          time.Duration
	SQSRetryMax           time.Duration
	SQSDLQMonitorInterval time.Duration // intervalo de leitura da profundidade da DLQ

	// Outbox.
	OutboxEnabled      bool
	OutboxPollInterval time.Duration
	OutboxBatchSize    int
	OutboxLease        time.Duration
	OutboxRetryBase    time.Duration
	OutboxRetryMax     time.Duration

	// Referências pendentes.
	PendingEnabled      bool
	PendingPollInterval time.Duration
	PendingBatchSize    int
	PendingLease        time.Duration
	PendingTTL          time.Duration
	PendingMaxAttempts  int
	PendingBackoffBase  time.Duration
	PendingBackoffMax   time.Duration

	// Injeção de falhas (somente testes). Ex.: "consumer.after_commit=exit".
	FaultPoints string
}

// FromEnv lê o ambiente, aplica os padrões e valida.
func FromEnv() (Config, error) {
	host, _ := os.Hostname()
	r := reader{}
	c := Config{
		InstanceID:         r.str("INSTANCE_ID", fmt.Sprintf("%s-%d", host, os.Getpid())),
		LogLevel:           r.str("LOG_LEVEL", "info"),
		HTTPAddr:           r.str("HTTP_ADDR", ":8081"),
		HTTPRequestTimeout: r.dur("HTTP_REQUEST_TIMEOUT", 10*time.Second),
		ShutdownTimeout:    r.dur("SHUTDOWN_TIMEOUT", 25*time.Second),

		DatabaseURL:           r.str("DATABASE_URL", ""),
		MigrationsDatabaseURL: r.str("MIGRATIONS_DATABASE_URL", ""),
		DBMaxConns:            int32(r.int("DB_MAX_CONNS", 20)),
		DBLockTimeout:         r.dur("DB_LOCK_TIMEOUT", 5*time.Second),

		OIDCIssuer:   r.str("OIDC_ISSUER", ""),
		OIDCJWKSURL:  r.str("OIDC_JWKS_URL", ""),
		OIDCAudience: r.str("OIDC_AUDIENCE", "wallet-service"),

		AWSRegion:             r.str("AWS_REGION", "us-east-1"),
		AWSEndpointURL:        r.str("AWS_ENDPOINT_URL", ""),
		SQSQueueName:          r.str("SQS_QUEUE_NAME", "wager-transactions.fifo"),
		SQSDLQName:            r.str("SQS_DLQ_NAME", "wager-transactions-dlq.fifo"),
		SQSEventsQueueName:    r.str("SQS_EVENTS_QUEUE_NAME", "wallet-events.fifo"),
		SQSConsumerEnabled:    r.bool("SQS_CONSUMER_ENABLED", true),
		SQSConsumerName:       r.str("SQS_CONSUMER_NAME", "wager-transactions-consumer"),
		SQSConsumerWorkers:    r.int("SQS_CONSUMER_WORKERS", 4),
		SQSWaitSeconds:        int32(r.int("SQS_WAIT_SECONDS", 10)),
		SQSMaxMessages:        int32(r.int("SQS_MAX_MESSAGES", 10)),
		SQSVisibilityTimeout:  int32(r.int("SQS_VISIBILITY_TIMEOUT", 30)),
		SQSRetryBase:          r.dur("SQS_RETRY_BASE", 2*time.Second),
		SQSRetryMax:           r.dur("SQS_RETRY_MAX", 60*time.Second),
		SQSDLQMonitorInterval: r.dur("SQS_DLQ_MONITOR_INTERVAL", 15*time.Second),

		OutboxEnabled:      r.bool("OUTBOX_ENABLED", true),
		OutboxPollInterval: r.dur("OUTBOX_POLL_INTERVAL", 500*time.Millisecond),
		OutboxBatchSize:    r.int("OUTBOX_BATCH_SIZE", 50),
		OutboxLease:        r.dur("OUTBOX_LEASE", 30*time.Second),
		OutboxRetryBase:    r.dur("OUTBOX_RETRY_BASE", time.Second),
		OutboxRetryMax:     r.dur("OUTBOX_RETRY_MAX", 5*time.Minute),

		PendingEnabled:      r.bool("PENDING_ENABLED", true),
		PendingPollInterval: r.dur("PENDING_POLL_INTERVAL", time.Second),
		PendingBatchSize:    r.int("PENDING_BATCH_SIZE", 50),
		PendingLease:        r.dur("PENDING_LEASE", 30*time.Second),
		PendingTTL:          r.dur("PENDING_TTL", 10*time.Minute),
		PendingMaxAttempts:  r.int("PENDING_MAX_ATTEMPTS", 20),
		PendingBackoffBase:  r.dur("PENDING_BACKOFF_BASE", time.Second),
		PendingBackoffMax:   r.dur("PENDING_BACKOFF_MAX", time.Minute),

		FaultPoints: r.str("FAULT_POINTS", ""),
	}
	if err := errors.Join(append(r.errs, c.Validate())...); err != nil {
		return Config{}, fmt.Errorf("invalid configuration: %w", err)
	}
	return c, nil
}

// Validate também é usada pelos testes, que montam a Config na mão.
func (c Config) Validate() error {
	var errs []error
	req := func(name, v string) {
		if strings.TrimSpace(v) == "" {
			errs = append(errs, fmt.Errorf("%s is required", name))
		}
	}
	req("DATABASE_URL", c.DatabaseURL)
	req("OIDC_ISSUER", c.OIDCIssuer)
	req("OIDC_JWKS_URL", c.OIDCJWKSURL)
	req("OIDC_AUDIENCE", c.OIDCAudience)
	req("HTTP_ADDR", c.HTTPAddr)
	req("SQS_QUEUE_NAME", c.SQSQueueName)
	req("SQS_DLQ_NAME", c.SQSDLQName)
	req("SQS_EVENTS_QUEUE_NAME", c.SQSEventsQueueName)
	req("SQS_CONSUMER_NAME", c.SQSConsumerName)

	pos := func(name string, ok bool) {
		if !ok {
			errs = append(errs, fmt.Errorf("%s must be positive", name))
		}
	}
	pos("DB_MAX_CONNS", c.DBMaxConns > 0)
	pos("SHUTDOWN_TIMEOUT", c.ShutdownTimeout > 0)
	pos("HTTP_REQUEST_TIMEOUT", c.HTTPRequestTimeout > 0)
	pos("SQS_CONSUMER_WORKERS", c.SQSConsumerWorkers > 0)
	pos("SQS_VISIBILITY_TIMEOUT", c.SQSVisibilityTimeout > 0)
	pos("SQS_DLQ_MONITOR_INTERVAL", c.SQSDLQMonitorInterval > 0)
	pos("OUTBOX_BATCH_SIZE", c.OutboxBatchSize > 0)
	pos("OUTBOX_LEASE", c.OutboxLease > 0)
	pos("PENDING_TTL", c.PendingTTL > 0)
	pos("PENDING_MAX_ATTEMPTS", c.PendingMaxAttempts > 0)
	pos("PENDING_BACKOFF_BASE", c.PendingBackoffBase > 0)
	pos("PENDING_LEASE", c.PendingLease > 0)

	if c.SQSWaitSeconds < 0 || c.SQSWaitSeconds > 20 {
		errs = append(errs, errors.New("SQS_WAIT_SECONDS must be between 0 and 20"))
	}
	if c.SQSMaxMessages < 1 || c.SQSMaxMessages > 10 {
		errs = append(errs, errors.New("SQS_MAX_MESSAGES must be between 1 and 10"))
	}
	if c.PendingBackoffMax < c.PendingBackoffBase {
		errs = append(errs, errors.New("PENDING_BACKOFF_MAX must be >= PENDING_BACKOFF_BASE"))
	}
	if c.OutboxRetryMax < c.OutboxRetryBase {
		errs = append(errs, errors.New("OUTBOX_RETRY_MAX must be >= OUTBOX_RETRY_BASE"))
	}
	return errors.Join(errs...)
}

// reader junta os erros para mostrar todos de uma vez.
type reader struct{ errs []error }

func (r *reader) str(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

func (r *reader) int(key string, def int) int {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		r.errs = append(r.errs, fmt.Errorf("%s: %w", key, err))
	}
	return n
}

func (r *reader) bool(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		r.errs = append(r.errs, fmt.Errorf("%s: %w", key, err))
	}
	return b
}

// dur aceita o formato do Go: "500ms", "10s", "5m".
func (r *reader) dur(key string, def time.Duration) time.Duration {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		r.errs = append(r.errs, fmt.Errorf("%s: %w", key, err))
	}
	return d
}
