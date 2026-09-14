package main

import (
	"github.com/scryer-media/usenet-bench/internal/benchmark"
	"github.com/scryer-media/usenet-bench/internal/fixture"
)

// Each axis is a separate view of the same declared corpus, not another
// independent sample. Preserve class/host/link/transport/storage strata and
// every DNF in the subgroup that owns it.
func subgroupAccounts(parents map[aggregateStratum]*aggregateAccount, cases map[string]fixture.ArchiveCase) map[aggregateStratum]*aggregateAccount {
	result := map[aggregateStratum]*aggregateAccount{}
	for key, parent := range parents {
		ids := map[string]bool{}
		for id := range parent.samples {
			ids[id] = true
		}
		for _, id := range parent.withheld {
			ids[id] = true
		}
		for id := range ids {
			c := cases[id]
			repair := "clean"
			if c.RepairProfile != "" && c.RepairProfile != fixture.CleanRepairProfile {
				repair = "repair_required"
			}
			encryption := string(c.Encryption)
			if encryption == "" {
				encryption = "unspecified"
			}
			for axis, value := range map[string]string{"repair": repair, "encryption": encryption} {
				group := key
				group.GroupAxis = axis
				group.GroupValue = value
				account := result[group]
				if account == nil {
					account = &aggregateAccount{samples: map[string][]benchmark.PairedSample{}}
					result[group] = account
				}
				if samples, ok := parent.samples[id]; ok {
					account.samples[id] = samples
				} else {
					account.withheld = append(account.withheld, id)
				}
			}
		}
	}
	return result
}
