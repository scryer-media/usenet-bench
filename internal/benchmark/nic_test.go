package benchmark

import "testing"

// The host counter is the real-provider leg's only independent witness, and it
// is read out of text two kernels format differently. A parser that silently
// counts loopback, or counts one interface twice, would inflate the witness
// and make a clean arm look contaminated.
func TestProcNetDevReceiveBytesSkipsLoopback(t *testing.T) {
	contents := `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes
    lo: 5000000    1000    0    0    0     0          0         0  5000000
  eth0: 1000000     500    0    0    0     0          0         0   250000
  eth1:  500000     250    0    0    0     0          0         0   125000
`
	total, err := parseProcNetDevReceiveBytes(contents)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1_500_000 {
		t.Fatalf("received bytes = %d, want the two non-loopback interfaces summed", total)
	}
}

func TestProcNetDevReceiveBytesRefusesAFileWithNoInterface(t *testing.T) {
	if _, err := parseProcNetDevReceiveBytes("Inter-|   Receive\n face |bytes\n"); err == nil {
		t.Fatal("a file naming no interface was read as a zero counter")
	}
}

// macOS prints one row per address family, all carrying the same
// per-interface totals. Counting each row would multiply the witness by the
// number of addresses the host happens to have configured.
func TestNetstatReceiveBytesCountsEachInterfaceOnce(t *testing.T) {
	contents := `Name  Mtu   Network       Address            Ipkts Ierrs     Ibytes    Opkts Oerrs     Obytes  Coll
lo0   16384 <Link#1>                            10     0       4096       10     0       4096     0
en0   1500  <Link#2>    aa:bb:cc:dd:ee:ff     1000     0    1000000      500     0     250000     0
en0   1500  192.168.1   192.168.1.10          1000     0    1000000      500     0     250000     0
en1   1500  <Link#3>    aa:bb:cc:dd:ee:00      500     0     500000      250     0     125000     0
`
	total, err := parseNetstatReceiveBytes(contents)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1_500_000 {
		t.Fatalf("received bytes = %d, want each non-loopback interface counted once", total)
	}
}

// An absent counter is not evidence of a clean arm, and a few percent over the
// payload is protocol overhead rather than contamination.
func TestNICExceedsPayloadOnlyOnRealExcess(t *testing.T) {
	delta := NICReceiveDelta{Available: true, DeltaBytes: 1_030_000}
	if _, exceeds := delta.ExceedsPayload(1_000_000); exceeds {
		t.Fatal("3% of protocol overhead was flagged as contamination")
	}
	if _, exceeds := (NICReceiveDelta{Available: true, DeltaBytes: 1_400_000}).ExceedsPayload(1_000_000); !exceeds {
		t.Fatal("40% of foreign traffic was not flagged")
	}
	if _, exceeds := (NICReceiveDelta{Unavailable: "no counter"}).ExceedsPayload(1_000_000); exceeds {
		t.Fatal("an unavailable counter was read as a verdict")
	}
}
