// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package interp_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
)

func TestWaitBackground(t *testing.T) {
	t.Parallel()
	for _, script := range []string{
		"hold &", "(hold &)", "{ hold & } | true",
		"eval '(hold &)'", "value=$(hold &)", "(true; (hold &) &)",
	} {
		t.Run(script, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			ready, stopped := make(chan struct{}), make(chan struct{})
			runner, err := interp.New(interp.ExecHandler(func(ctx context.Context, args []string) error {
				if args[0] != "hold" {
					return fmt.Errorf("unexpected command: %q", args)
				}
				close(ready)
				<-ctx.Done()
				close(stopped)
				return ctx.Err()
			}))
			if err != nil {
				t.Fatal(err)
			}
			if err := runner.Run(ctx, parse(t, nil, script)); err != nil {
				t.Fatal(err)
			}
			select {
			case <-ready:
			case <-time.After(5 * time.Second):
				t.Fatal("background job did not start")
			}
			joined := make(chan struct{})
			go func() { runner.WaitBackground(); close(joined) }()
			select {
			case <-joined:
				t.Fatal("wait returned while the job was active")
			case <-time.After(20 * time.Millisecond):
			}
			cancel()
			select {
			case <-joined:
			case <-time.After(5 * time.Second):
				t.Fatal("wait did not join the canceled job")
			}
			select {
			case <-stopped:
			default:
				t.Fatal("wait returned before the job stopped")
			}
			runner.WaitBackground()
			runner.Reset()
			if err := runner.Run(t.Context(), parse(t, nil, "true")); err != nil {
				t.Fatal(err)
			}
			runner.WaitBackground()
		})
	}
}

func TestWaitBackgroundProcessSubstitution(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process substitution is not supported on Windows")
	}
	t.Parallel()
	root := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	runner, err := interp.New(interp.Env(expand.ListEnviron("TMPDIR=" + root)))
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(ctx, parse(t, nil, "printf '%s' <(printf hello)")); err != nil {
		t.Fatal(err)
	}
	cancel()
	runner.WaitBackground()
	entries, err := os.ReadDir(filepath.Clean(root))
	if err != nil || len(entries) != 0 {
		t.Fatalf("process substitution was not cleaned up: %v, %v", entries, err)
	}
}
