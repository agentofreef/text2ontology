package job

import (
	"fmt"
	"sync"
)

// dispatcher holds the kind→Handler map. Workers look up the handler at
// claim time; missing handlers cause the job to be marked failed with
// 'no_handler_registered'.
type dispatcher struct {
	mu       sync.RWMutex
	handlers map[Kind]Handler
}

func newDispatcher() *dispatcher {
	return &dispatcher{handlers: make(map[Kind]Handler)}
}

func (d *dispatcher) register(k Kind, h Handler) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.handlers[k] = h
}

func (d *dispatcher) lookup(k Kind) (Handler, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	h, ok := d.handlers[k]
	if !ok {
		return nil, fmt.Errorf("no handler registered for kind %q", k)
	}
	return h, nil
}
