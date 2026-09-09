package adversarial

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestPAR2ConstantsAndDistinctRecoveryBoundaries(t *testing.T) {
	if got := parConstants(8); !reflect.DeepEqual(got, []uint16{2, 4, 16, 128, 256, 2048, 8192, 16384}) {
		t.Fatalf("GF constants: %v", got)
	}
	a, _ := Generate("repair-multi-exactly-sufficient")
	b, _ := Generate("repair-multi-surplus-recovery")
	if Digest(a.Data["payload-002.bin"]) == Digest(b.Data["payload-002.bin"]) {
		t.Fatal("sufficient and surplus recipes are identical")
	}
}

func TestIndependentMultiSlicePAR2Oracle(t *testing.T) {
	if os.Getenv("ADVERSARIAL_ORACLES") != "1" {
		t.Skip("requires installed independent par2 oracle")
	}
	tool, err := exec.LookPath("par2")
	if err != nil {
		t.Fatal(err)
	}
	for _, variant := range []string{"exactly-sufficient", "surplus-recovery", "insufficient", "dependent-equations"} {
		t.Run(variant, func(t *testing.T) {
			b, err := Generate("repair-multi-" + variant)
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			for i, name := range []string{"alpha.bin", "beta.bin", "fixture.par2"} {
				if err := os.WriteFile(filepath.Join(dir, name), b.Data[[]string{"payload-000.bin", "payload-001.bin", "payload-002.bin"}[i]], 0600); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, tool, "r", "-q", "-t1", "fixture.par2")
			cmd.Dir = dir
			cmd.WaitDelay = time.Second
			log := &boundedOracleOutput{}
			cmd.Stdout = log
			cmd.Stderr = log
			err = cmd.Run()
			if b.Manifest.Case.Expectation == Reject {
				if err == nil || ctx.Err() != nil || cmd.ProcessState == nil || !cmd.ProcessState.Exited() || cmd.ProcessState.ExitCode() != 2 {
					t.Fatalf("expected independent insufficient-recovery exit 2, not a crash or timeout: %v %s", err, log.data)
				}
				return
			}
			if err != nil {
				t.Fatalf("independent repair failed: %v %s", err, log.data)
			}
			for name, digest := range b.Manifest.ExpectedOutputs {
				data, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil || Digest(data) != digest {
					t.Fatalf("independent repaired output differs: %s %v", name, err)
				}
			}
		})
	}
}

type boundedOracleOutput struct{ data []byte }

func (b *boundedOracleOutput) Write(data []byte) (int, error) {
	n := len(data)
	remaining := (1 << 20) - len(b.data)
	if remaining > 0 {
		b.data = append(b.data, data[:min(remaining, n)]...)
	}
	return n, nil
}
