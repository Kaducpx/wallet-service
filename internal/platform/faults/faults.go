// Package faults simula quedas nos testes de recuperação.
//
// FAULT_POINTS="ponto=ação,ponto=ação":
//
//	consumer.after_commit=exit   derruba o processo na hora (os.Exit)
//	wager.submit=fail:3          falha (erro passageiro) nas 3 primeiras vezes
//	wager.submit=fail            falha sempre
//
// Sem FAULT_POINTS (produção), não faz nada.
package faults

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"wallet-service/internal/app"
)

// Código de saída das quedas simuladas (para os testes distinguirem de uma
// falha real).
const ExitCode = 97

// Injector guarda as regras por ponto. O mutex protege o contador, porque
// várias goroutines podem passar pelo mesmo ponto.
type Injector struct {
	mu     sync.Mutex
	points map[string]*rule
}

type rule struct {
	action    string // exit | fail
	remaining int    // -1 = infinito
}

func Parse(spec string) (*Injector, error) {
	in := &Injector{points: map[string]*rule{}}
	for _, item := range strings.Split(spec, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		point, action, ok := strings.Cut(item, "=")
		if !ok {
			return nil, fmt.Errorf("invalid fault spec %q", item)
		}
		r := &rule{remaining: -1}
		name, count, hasCount := strings.Cut(action, ":")
		r.action = name
		if hasCount {
			n, err := strconv.Atoi(count)
			if err != nil {
				return nil, fmt.Errorf("invalid fault count %q", item)
			}
			r.remaining = n
		}
		if r.action != "exit" && r.action != "fail" {
			return nil, fmt.Errorf("unknown fault action %q", r.action)
		}
		in.points[point] = r
	}
	return in, nil
}

func (in *Injector) Hit(point string) error {
	in.mu.Lock()
	r, ok := in.points[point]
	if !ok || r.remaining == 0 {
		in.mu.Unlock()
		return nil
	}
	if r.remaining > 0 {
		r.remaining--
	}
	action := r.action
	in.mu.Unlock()

	switch action {
	case "exit":
		fmt.Fprintf(os.Stderr, `{"level":"ERROR","msg":"fault injected: abrupt exit","point":%q}`+"\n", point)
		os.Exit(ExitCode)
	case "fail":
		return fmt.Errorf("%w: fault injected at %s", app.ErrUnavailable, point)
	}
	return nil
}
