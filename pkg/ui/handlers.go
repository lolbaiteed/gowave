package ui

import "sync/atomic"

// Handler registry — maps string IDs to Go callbacks.
//
// No sync.Mutex: WASM is single-threaded so there is no concurrent access.
// On the server, ClearHandlers() is called before each render, so the
// registry is always fresh per request.

var (
	handlers      = make(map[string]func())
	inputHandlers = make(map[string]func(string))
	seq           atomic.Uint64
)

func nextID() string {
	return "gw" + uitoa(seq.Add(1))
}

// uitoa converts uint64 to decimal string without importing fmt.
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

func registerHandler(fn func()) string {
	id := nextID()
	handlers[id] = fn
	return id
}

func registerInputHandler(fn func(string)) string {
	id := nextID()
	inputHandlers[id] = fn
	return id
}

// Dispatch is called by the WASM bridge to invoke a handler by ID.
func Dispatch(event, id, value string) {
	switch event {
	case "click", "change", "enter":
		if fn := handlers[id]; fn != nil {
			fn()
		}
	case "input":
		if fn := inputHandlers[id]; fn != nil {
			fn(value)
		}
	}
}

// ClearHandlers resets the registry between render cycles.
func ClearHandlers() {
	handlers = make(map[string]func())
	inputHandlers = make(map[string]func(string))
	seq.Store(0)
}
