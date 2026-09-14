package benchmark

import "os"

// WeaverDiagnosticEnvironment names the diagnostic switches an operator may
// set on the adapter process for Weaver to see. None is set by default, and
// none tells Weaver anything about the server: they pick an implementation or
// turn on logging and profiling. Each one set is rendered into the product
// environment, so the audit record shows the run was a diagnostic one.
var WeaverDiagnosticEnvironment = []string{
	// TLS implementation override.
	"WEAVER_NNTP_TLS_BACKEND",
	// Log filter.
	"RUST_LOG",
	// Weaver's hot-path profiler: periodic wall, CPU and value bucket tables.
	"WEAVER_PROFILE_HOT_PATHS",
	"WEAVER_PROFILE_HOT_PATHS_INTERVAL_SECS",
	"WEAVER_PROFILE_HOT_PATHS_TOP_N",
	// The RAR decoder's phase and aggregate decode markers, on stderr.
	"RARPAR_BENCH_PHASES",
	// Forces the RAR decoder onto its single-threaded path, for an A/B.
	"UNRAR_RS_DISABLE_PARALLEL",
}

// WeaverDiagnosticOverrides returns KEY=VALUE for every diagnostic switch set
// in this process's environment, in WeaverDiagnosticEnvironment order.
func WeaverDiagnosticOverrides() []string {
	var env []string
	for _, key := range WeaverDiagnosticEnvironment {
		if value := os.Getenv(key); value != "" {
			env = append(env, key+"="+value)
		}
	}
	return env
}
