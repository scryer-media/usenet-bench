package adversarial

import "fmt"

// VerifyOutputWithMetadata validates declared client-owned bookkeeping bytes
// separately from generated payload expectations. Nothing is removed or renamed;
// metadata cannot shadow a payload or silently exempt arbitrary extra output.
func VerifyOutputWithMetadata(path string, expected, metadata map[string]string, limits Limits) error {
	if len(expected) == 0 {
		return fmt.Errorf("recipe has no output oracle")
	}
	combined := make(map[string]string, len(expected)+len(metadata))
	for name, digest := range expected {
		combined[name] = digest
	}
	for name, digest := range metadata {
		if !safeName(name) || !validDigest(digest) {
			return fmt.Errorf("invalid declared output metadata")
		}
		if _, ok := combined[name]; ok {
			return fmt.Errorf("metadata shadows expected payload %s", name)
		}
		combined[name] = digest
	}
	return VerifyOutput(path, combined, limits)
}
