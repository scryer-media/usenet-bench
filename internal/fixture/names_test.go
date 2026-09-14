package fixture

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFixtureNamesDescribeDimensions(t *testing.T) {
	m, err := LoadMatrix("../../fixtures/matrix.json")
	if err != nil {
		t.Fatal(err)
	}
	cases, err := m.Expand()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for _, c := range cases {
		if previous := seen[c.DisplayName()]; previous != "" {
			t.Fatalf("two fixtures share a display name: %s and %s: %s", previous, c.ID, c.DisplayName())
		}
		seen[c.DisplayName()] = c.ID
		if len(c.DisplayName()) < 10 || c.DisplayName() == c.ID || strings.Contains(c.DisplayName(), "Custom workload") {
			t.Fatalf("missing readable name for %s", c.ID)
		}
		raw, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		var got ArchiveCase
		if err = json.Unmarshal(raw, &got); err != nil || got != c {
			t.Fatalf("name changed fixture identity: %s: %v", c.ID, err)
		}
		if !strings.Contains(string(raw), `"display_name"`) {
			t.Fatal("missing serialized name")
		}
	}
}

func TestRAR4EncryptedWriterErasHaveDistinctNames(t *testing.T) {
	legacy := ArchiveCase{ID: "rar4-393-data-normal-solid-data-incompressible", ArchiveFormat: RAR4, GeneratorToolchain: "rarlab-3.93", Compression: Normal, Solid: true, Encryption: DataEncryption, Payload: IncompressiblePayload}
	newer := legacy
	newer.ID = "rar4-data-normal-solid-data-incompressible"
	newer.GeneratorToolchain = "rarlab-4.20"
	if legacy.DisplayName() == newer.DisplayName() || !strings.Contains(legacy.DisplayName(), "RAR 3.93") || !strings.Contains(newer.DisplayName(), "RAR 4.20") {
		t.Fatalf("writer eras collapsed: %s / %s", legacy.DisplayName(), newer.DisplayName())
	}
	for _, c := range []ArchiveCase{legacy, newer} {
		if !strings.Contains(c.DisplayName(), "encrypted contents") || !strings.Contains(c.DisplayName(), "normal compression") {
			t.Fatal(c.DisplayName())
		}
	}
}
