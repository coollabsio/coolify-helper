package scan

import (
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/coollabsio/coolify-helper/src/affected/internal/testrepo"
)

// TestScanPerformance scans an application whose closure is nearly the whole
// synthetic repository (50k files), with GOMAXPROCS 2 like the 2 vCPU of the
// brief's budget (10 s). It logs the time; it fails at 4x the budget.
func TestScanPerformance(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	dir := t.TempDir()
	s, err := testrepo.BuildSynth(dir, testrepo.DefaultSynth)
	if err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(dir, "wt")
	if out, err := exec.Command("git", "clone", "-q", s.Repo.Dir, wt).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	head, _ := exec.Command("git", "-C", wt, "rev-parse", "HEAD").Output()
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(2))
	req := scanRequest(wt, strings.TrimSpace(string(head)), "pnpm turbo run build --filter=@synth/pkg499")
	start := time.Now()
	resp := Run(context.Background(), req, Options{ToolVersion: "t"})
	took := time.Since(start)
	t.Logf("scan of a 50k-file closure on 2 CPUs: %s, %d references, widened %v", took, len(resp.ReferencePaths), resp.Widened)
	if len(resp.Widened) > 0 {
		t.Fatalf("widened: %v", resp.Widened)
	}
	if took > 40*time.Second {
		t.Errorf("took %s, the budget is 10s", took)
	}
}
