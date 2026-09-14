package main

import (
	"fmt"
	"strings"

	"github.com/scryer-media/usenet-bench/internal/benchmark"
)

// A phase's name remains its stable selector. Its display name explains the
// experiment instead of requiring users to decode a historical abbreviation.
func (p ChainPhase) displayName() string {
	parts := []string{strings.TrimSpace(p.DisplayName)}
	if parts[0] == "" {
		parts[0] = map[string]string{"sequential": "Individual download performance", "queue": "Queued download performance", "queue-transition": "Queue drain and job handoff"}[p.Mode]
		if parts[0] == "" {
			parts[0] = "Download performance"
		}
	}
	if s := p.PlanSpec; s != nil {
		for _, transport := range s.Transports {
			if transport == "tls" {
				parts = append(parts, "TLS")
			} else if transport == "plaintext" {
				parts = append(parts, "unencrypted NNTP")
			}
		}
		if s.Profile == benchmark.ProfileStock {
			parts = append(parts, "stock settings")
		} else if s.Profile == benchmark.ProfileEquivalentThroughput {
			parts = append(parts, "matched throughput settings")
		}
		if s.StorageProfile == "nfs-complete" {
			parts = append(parts, "network-storage output")
		}
	}
	if p.ServerLink != "" {
		parts = append(parts, strings.ReplaceAll(p.ServerLink, "gbit", " Gbit/s"))
	}
	parts = append(parts, chainRTTLabel(p.ServerRTT)+" round-trip latency")
	article, err := benchmark.ResolveArticleProfile(chainArticleSizeLabel(p.ArticleSize))
	if err == nil {
		parts = append(parts, fmt.Sprintf("%d KiB articles", article.RawBytes/1024))
	}
	return strings.Join(parts, " · ")
}

func (p ChainPhase) label() string { return p.displayName() + " [" + p.Name + "]" }

func comparisonDisplayName(workload string, s comparisonStratum) string {
	transport := "unencrypted NNTP"
	if s.Transport == benchmark.TLS {
		transport = "TLS"
	}
	return fmt.Sprintf("%s · %s · %.3g Gbit/s · %.3g ms round-trip latency · %d KiB articles", workload, transport, float64(s.ServerEgressBPS)/1e9, float64(s.ServerRTTMicros)/1000, s.ArticleRawBytes/1024)
}
