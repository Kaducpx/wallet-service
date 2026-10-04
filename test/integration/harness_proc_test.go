//go:build integration

package integration

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"testing"
	"time"

	"wallet-service/internal/platform/config"
)

// Instâncias em processos independentes (binário compilado), para os cenários
// multi-instância e de queda abrupta.

// proc é uma instância em um processo independente (memória e conexões
// próprias), usada nos cenários multi-instância e de queda abrupta.
type proc struct {
	cmd    *exec.Cmd
	base   string
	out    *syncBuffer
	done   chan struct{}
	exitCh int
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// freePort descobre uma porta TCP livre para um processo de teste.
func freePort(t testing.TB) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// configEnv converte a Config em variáveis de ambiente para o subprocesso.
func configEnv(c config.Config) []string {
	b := strconv.FormatBool
	d := func(x time.Duration) string { return x.String() }
	return []string{
		"INSTANCE_ID=" + c.InstanceID, "LOG_LEVEL=" + c.LogLevel, "HTTP_ADDR=" + c.HTTPAddr,
		"SHUTDOWN_TIMEOUT=" + d(c.ShutdownTimeout), "HTTP_REQUEST_TIMEOUT=" + d(c.HTTPRequestTimeout), "DATABASE_URL=" + c.DatabaseURL,
		"DB_MAX_CONNS=" + strconv.Itoa(int(c.DBMaxConns)), "DB_LOCK_TIMEOUT=" + d(c.DBLockTimeout),
		"OIDC_ISSUER=" + c.OIDCIssuer, "OIDC_JWKS_URL=" + c.OIDCJWKSURL, "OIDC_AUDIENCE=" + c.OIDCAudience,
		"AWS_REGION=" + c.AWSRegion, "AWS_ENDPOINT_URL=" + c.AWSEndpointURL,
		"SQS_QUEUE_NAME=" + c.SQSQueueName, "SQS_DLQ_NAME=" + c.SQSDLQName, "SQS_EVENTS_QUEUE_NAME=" + c.SQSEventsQueueName,
		"SQS_CONSUMER_ENABLED=" + b(c.SQSConsumerEnabled), "SQS_CONSUMER_NAME=" + c.SQSConsumerName,
		"SQS_CONSUMER_WORKERS=" + strconv.Itoa(c.SQSConsumerWorkers), "SQS_WAIT_SECONDS=" + strconv.Itoa(int(c.SQSWaitSeconds)),
		"SQS_MAX_MESSAGES=" + strconv.Itoa(int(c.SQSMaxMessages)), "SQS_VISIBILITY_TIMEOUT=" + strconv.Itoa(int(c.SQSVisibilityTimeout)),
		"SQS_RETRY_BASE=" + d(c.SQSRetryBase), "SQS_RETRY_MAX=" + d(c.SQSRetryMax), "SQS_DLQ_MONITOR_INTERVAL=" + d(c.SQSDLQMonitorInterval),
		"OUTBOX_ENABLED=" + b(c.OutboxEnabled), "OUTBOX_POLL_INTERVAL=" + d(c.OutboxPollInterval),
		"OUTBOX_BATCH_SIZE=" + strconv.Itoa(c.OutboxBatchSize), "OUTBOX_LEASE=" + d(c.OutboxLease),
		"OUTBOX_RETRY_BASE=" + d(c.OutboxRetryBase), "OUTBOX_RETRY_MAX=" + d(c.OutboxRetryMax),
		"PENDING_ENABLED=" + b(c.PendingEnabled), "PENDING_POLL_INTERVAL=" + d(c.PendingPollInterval),
		"PENDING_BATCH_SIZE=" + strconv.Itoa(c.PendingBatchSize), "PENDING_LEASE=" + d(c.PendingLease),
		"PENDING_TTL=" + d(c.PendingTTL), "PENDING_MAX_ATTEMPTS=" + strconv.Itoa(c.PendingMaxAttempts),
		"PENDING_BACKOFF_BASE=" + d(c.PendingBackoffBase), "PENDING_BACKOFF_MAX=" + d(c.PendingBackoffMax),
		"FAULT_POINTS=" + c.FaultPoints,
	}
}

// startProc sobe o binário com a configuração dada e espera o readiness.
func startProc(t testing.TB, cfg config.Config) *proc {
	t.Helper()
	port := freePort(t)
	cfg.HTTPAddr = fmt.Sprintf("127.0.0.1:%d", port)
	p := &proc{base: fmt.Sprintf("http://127.0.0.1:%d", port), out: &syncBuffer{}, done: make(chan struct{})}
	p.cmd = newCmd(cfg, p.out)
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		_ = p.cmd.Wait()
		p.exitCh = p.cmd.ProcessState.ExitCode()
		close(p.done)
	}()
	t.Cleanup(func() {
		p.kill()
		if t.Failed() {
			t.Logf("logs of %s:\n%s", cfg.InstanceID, tail(p.out.String(), 4000))
		}
	})
	eventually(t, 60*time.Second, func() bool {
		select {
		case <-p.done:
			t.Fatalf("process exited during startup (code %d):\n%s", p.exitCh, tail(p.out.String(), 4000))
		default:
		}
		resp, err := http.Get(p.base + "/health/ready")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, "process ready")
	return p
}

// newCmd prepara a execução do binário com a configuração informada.
func newCmd(cfg config.Config, out io.Writer) *exec.Cmd {
	cmd := exec.Command(binaryPath)
	cmd.Env = append(os.Environ(), configEnv(cfg)...)
	cmd.Stdout, cmd.Stderr = out, out
	return cmd
}

// kill encerra o processo abruptamente (equivalente a SIGKILL).
func (p *proc) kill() {
	select {
	case <-p.done:
		return
	default:
	}
	_ = p.cmd.Process.Kill()
	<-p.done
}

// waitExit espera o processo terminar sozinho (queda injetada).
func (p *proc) waitExit(t testing.TB, timeout time.Duration) int {
	t.Helper()
	select {
	case <-p.done:
		return p.exitCh
	case <-time.After(timeout):
		t.Fatalf("process did not exit within %s", timeout)
		return -1
	}
}

// tail devolve os últimos n caracteres (para mostrar logs quando o teste falha).
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// startProcNoWait sobe um processo que deve cair sozinho (não espera ready).
func startProcNoWait(t *testing.T, cfg config.Config) *proc {
	t.Helper()
	cfg.HTTPAddr = fmt.Sprintf("127.0.0.1:%d", freePort(t))
	p := &proc{out: &syncBuffer{}, done: make(chan struct{})}
	p.cmd = newCmd(cfg, p.out)
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		_ = p.cmd.Wait()
		p.exitCh = p.cmd.ProcessState.ExitCode()
		close(p.done)
	}()
	t.Cleanup(p.kill)
	return p
}
