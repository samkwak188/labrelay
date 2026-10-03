package fault

import "os"

// Fault injection is opt-in and process-scoped; no HTTP endpoint enables it.
func Hit(name string) {
	if os.Getenv("LABRELAY_FAULT") == name {
		os.Exit(86)
	}
}
