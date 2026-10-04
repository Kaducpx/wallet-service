package worker

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Loop chama tick de tempos em tempos. Se tick disser que tinha trabalho,
// roda de novo na hora; se não, espera o intervalo.
//
// No Stop, para de começar rodadas novas e deixa a atual terminar dentro do
// prazo. Só se o prazo acabar a rodada é cancelada. Done() fecha quando a
// goroutine termina de fato.
type Loop struct {
	name     string
	interval time.Duration
	tick     func(ctx context.Context) (busy bool)
	log      *slog.Logger

	stopCtx    context.Context // cancelado no Stop: não inicia novas iterações
	stop       context.CancelFunc
	workCtx    context.Context // cancelado só quando o prazo do Stop vence
	cancelWork context.CancelFunc
	done       chan struct{} // fechado quando a goroutine terminou
	once       sync.Once     // garante que Stop só execute uma vez
}

func NewLoop(name string, interval time.Duration, log *slog.Logger, tick func(ctx context.Context) bool) *Loop {
	return &Loop{name: name, interval: interval, tick: tick, log: log.With(slog.String("worker", name)), done: make(chan struct{})}
}

// Os contextos não vêm do ctx do OnStart porque o Fx cancela esse ctx logo
// depois de subir.
func (l *Loop) Start() {
	l.stopCtx, l.stop = context.WithCancel(context.Background())
	l.workCtx, l.cancelWork = context.WithCancel(context.Background())
	go l.run()
	l.log.Info("worker started")
}

func (l *Loop) run() {
	defer close(l.done)
	for {
		if l.stopCtx.Err() != nil {
			return
		}
		busy := l.tick(l.workCtx)
		if busy {
			continue // havia trabalho: tenta de novo sem esperar
		}
		select {
		case <-l.stopCtx.Done():
			return
		case <-time.After(l.interval):
		}
	}
}

// Stop espera a rodada atual até o prazo de ctx; depois cancela.
func (l *Loop) Stop(ctx context.Context) error {
	var err error
	l.once.Do(func() {
		if l.stop == nil {
			close(l.done)
			return
		}
		l.stop()
		select {
		case <-l.done:
		case <-ctx.Done():
			l.log.Warn("worker stop deadline reached, cancelling in-flight work")
			l.cancelWork()
			<-l.done
			err = ctx.Err()
		}
		l.cancelWork()
		l.log.Info("worker stopped")
	})
	return err
}

func (l *Loop) Stopping() bool { return l.stopCtx != nil && l.stopCtx.Err() != nil }

func (l *Loop) Done() <-chan struct{} { return l.done }
