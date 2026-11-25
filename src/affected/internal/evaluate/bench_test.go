//go:build coolifytest

package evaluate_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/coollabsio/coolify-helper/src/affected/internal/api"
	. "github.com/coollabsio/coolify-helper/src/affected/internal/evaluate"
	"github.com/coollabsio/coolify-helper/src/affected/internal/testrepo"
)

var (
	synthOnce sync.Once
	synth     *testrepo.Synth
	synthDir  string
	synthErr  error
)

// loadSynth builds the synthetic repository of the brief once per test binary.
func loadSynth(tb testing.TB) *testrepo.Synth {
	tb.Helper()
	synthOnce.Do(func() {
		synthDir, synthErr = os.MkdirTemp("", "coolify-affected-synth-")
		if synthErr == nil {
			synth, synthErr = testrepo.BuildSynth(synthDir, testrepo.DefaultSynth)
		}
	})
	if synthErr != nil {
		tb.Fatal(synthErr)
	}
	return synth
}

func synthRequest(tb testing.TB, s *testrepo.Synth, after, base string) *api.EvaluateRequest {
	m := map[string]any{
		"schema_version": 1,
		"repository":     map[string]any{"url": s.Repo.URL, "mirror_key": goldenKey},
		"after":          s.Repo.Commits[after],
		"fetch_refs":     []string{"refs/heads/main"},
		"applications":   s.Applications(s.Repo.Commits[base]),
	}
	b, _ := json.Marshal(m)
	var req api.EvaluateRequest
	if err := json.Unmarshal(b, &req); err != nil {
		tb.Fatal(err)
	}
	if err := req.Validate(); err != nil {
		tb.Fatal(err)
	}
	return &req
}

// TestPerformanceTargets measures the targets of the brief on the synthetic
// repository (50k files, 500 members, 100 apps, 2,000 changed files) and logs
// the numbers. Run it with -v. It only fails on gross regressions (4x the
// targets), because CI machines vary.
func TestPerformanceTargets(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	s := loadSynth(t)
	mirrors := filepath.Join(t.TempDir(), "mirrors")

	var st Stats
	start := time.Now()
	cold := Run(context.Background(), synthRequest(t, s, "change", "base"), Options{MirrorRoot: mirrors, ToolVersion: "t", Stats: &st})
	coldTotal := time.Since(start)
	if len(cold.Errors) > 0 {
		t.Fatal(cold.Errors)
	}
	t.Logf("first fetch (local file:// remote): total %s, timings %v, blob round trips %d, blobs %d, mirror %d MB",
		coldTotal, cold.TimingsMS, st.BlobFetches, st.FetchedBlobs, dirSize(mirrors)>>20)

	// Warm mirror, 2,000 changed files, the graph cache cleared: every manifest is parsed again.
	_ = os.RemoveAll(filepath.Join(mirrors, goldenKey+".cache"))
	var peak runtime.MemStats
	runtime.GC()
	start = time.Now()
	warm := Run(context.Background(), synthRequest(t, s, "change", "base"), Options{MirrorRoot: mirrors, ToolVersion: "t", Stats: &st})
	warmTotal := time.Since(start)
	runtime.ReadMemStats(&peak)
	deploys := 0
	for _, a := range warm.Applications {
		if a.Decision == api.Deploy {
			deploys++
		}
	}
	t.Logf("warm, 100 apps, 2,000 changed files: total %s (fetch %dms, excluded from the target), timings %v, %d deploys, cache hit %v, heap in use %d MB",
		warmTotal, warm.TimingsMS["fetch"], warm.TimingsMS, deploys, st.GraphCacheHit, peak.HeapInuse>>20)
	if ex := warmTotal - time.Duration(warm.TimingsMS["fetch"])*time.Millisecond; ex > 12*time.Second {
		t.Errorf("warm evaluate took %s without the fetch, the target is 3s", ex)
	}

	// Warm mirror, a push that changes no manifest: no blob fetch, graph from the cache.
	start = time.Now()
	src := Run(context.Background(), synthRequest(t, s, "srconly", "change"), Options{MirrorRoot: mirrors, ToolVersion: "t", Stats: &st})
	srcTotal := time.Since(start)
	t.Logf("warm, no manifest change: total %s, timings %v, cache hit %v, blob round trips %d", srcTotal, src.TimingsMS, st.GraphCacheHit, st.BlobFetches)
	if !st.GraphCacheHit || st.BlobFetches != 0 {
		t.Errorf("no manifest change: cache hit %v, blob round trips %d; want a hit and none", st.GraphCacheHit, st.BlobFetches)
	}
	if srcTotal > 4*time.Second {
		t.Errorf("no manifest change took %s, the target is 1s", srcTotal)
	}
}

func dirSize(dir string) int64 {
	var n int64
	_ = filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			n += info.Size()
		}
		return nil
	})
	return n
}

// BenchmarkEvaluateWarm is the warm evaluate of the brief's target.
func BenchmarkEvaluateWarm(b *testing.B) {
	s := loadSynth(b)
	mirrors := filepath.Join(b.TempDir(), "mirrors")
	Run(context.Background(), synthRequest(b, s, "change", "base"), Options{MirrorRoot: mirrors, ToolVersion: "t"})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Run(context.Background(), synthRequest(b, s, "change", "base"), Options{MirrorRoot: mirrors, ToolVersion: "t"})
	}
}
