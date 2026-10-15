package postgres

import "errors"

// ErrLeaseLost is returned when the fence of a lease or outbox claim no longer
// belongs to the caller: the term expired and someone else took over, or the
// lease was never held.
var ErrLeaseLost = errors.New("lease lost")
