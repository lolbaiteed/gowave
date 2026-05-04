package ui

import (
	"sync"
	"sync/atomic"
)

// The handler registry maps string IDs (emitted as data-gw-* attributes)
// to Go functions. On the server, IDs are in HTML but never dispatched.
// In WASM, the JS bridge calls __gowave_dispatch(event, id, value) which
// looks up the handler here and calls it.

var (
	mu            sync.RWMutex
	handlers      = make(map[string]func())
	inputHandlers = make(map[string]func(string))
	seq           atomic.Uint64
)

func nextID() string {
	// Hand-rolled to avoid importing fmt, which TinyGo 0.42 transitively
	// links against net/http via its js/wasm shims.
	return "gw" + uitoa(seq.Add(1))
}

// uitoa converts a uint64 to its decimal string without importing fmt.
func uitoa(n uint64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[pos:])
}

// registerHandler stores a void handler and returns its ID.
func registerHandler(fn func()) string {
	id := nextID()
	mu.Lock()
	handlers[id] = fn
	mu.Unlock()
	return id
}

// registerInputHandler stores a string handler and returns its ID.
func registerInputHandler(fn func(string)) string {
	id := nextID()
	mu.Lock()
	inputHandlers[id] = fn
	mu.Unlock()
	return id
}

// Dispatch is called by the WASM bridge to invoke a handler by ID.
// event: "click" | "input" | "change" | "enter"
// id: the handler ID emitted in data-gw-*
// value: the input value (empty for click/change)
func Dispatch(event, id, value string) {
	switch event {
	case "click", "change", "enter":
		mu.RLock()
		fn := handlers[id]
		mu.RUnlock()
		if fn != nil {
			fn()
		}
	case "input":
		mu.RLock()
		fn := inputHandlers[id]
		mu.RUnlock()
		if fn != nil {
			fn(value)
		}
	}
}

// ClearHandlers resets the registry between renders.
// Called by the runtime before each render cycle.
func ClearHandlers() {
	mu.Lock()
	handlers = make(map[string]func())
	inputHandlers = make(map[string]func(string))
	mu.Unlock()
	seq.Store(0)
}
