// This file is the one place in this package that starts a program.
// internal/policy fails the build if another file imports os/exec. The
// programs only read facts: sw_vers, sysctl and nvidia-smi.

package platform

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"time"
)

// runTimeout caps each program. nvidia-smi can take a second on a cold
// driver; a fact that takes longer isn't worth the wait.
const runTimeout = 3 * time.Second

// run starts name with args and no shell, waits at most runTimeout, and
// returns its output trimmed, or "" when the program is missing, fails or
// runs too long.
func run(ctx context.Context, name string, args ...string) string {
	ctx, cancel := context.WithTimeout(ctx, runTimeout)
	// defer runs cancel when run returns, which frees the timer.
	defer cancel()
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...) // #nosec G204 -- fixed program names and arguments from this package
	cmd.Stdout = &out
	if cmd.Run() != nil {
		return ""
	}
	return strings.TrimSpace(out.String())
}
