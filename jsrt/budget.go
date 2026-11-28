package jsrt

// What a program that starts kario-jsrt itself (the mini-program host)
// sets and reads, as src/ spells it.
const (
	// BudgetEnv bounds one turn of JavaScript, in milliseconds (main.c).
	BudgetEnv = "KARIO_JSRT_BUDGET_MS"
	// Interrupted starts the line kario-jsrt writes on stderr when it
	// interrupted a turn over its budget; the turn's handler then reports
	// InterruptedError, uncaught.
	Interrupted      = "kario-jsrt: interrupted:"
	InterruptedError = "InternalError: interrupted"
	// Uncaught starts the line of an exception no handler caught, e.g.
	// `kario-jsrt: uncaught exception in on("action"): RangeError: x`
	// (dispatch.c, kario.c); QuickJS-ng's InternalErrors (out of memory,
	// interrupted) are always uncaught.
	Uncaught = "kario-jsrt: uncaught exception"
	// StorageFile is the storage of kario.storage in the data directory
	// (kario.c, store.c): an object of keys and their values' JSON text.
	StorageFile = "kario-storage.json"
	// StorageNotSaved is in the line the runtime writes on stderr when a
	// write of StorageFile failed, e.g. "kario-jsrt: storage not saved:
	// Permission denied"; the values stay in memory, storage.flush()
	// rejects.
	StorageNotSaved = "storage not saved:"
)
