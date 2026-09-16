// Copyright (c) 2019, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

//go:build unix

package interp_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"mvdan.cc/sh/v3/interp"
)

func TestRunnerTerminalStdIO(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		files func(*testing.T) (secondary io.Writer, primary io.Reader)
		want  string
	}{
		{"Nil", func(t *testing.T) (io.Writer, io.Reader) {
			return nil, strings.NewReader("\n")
		}, "\n"},
		{"Pipe", func(t *testing.T) (io.Writer, io.Reader) {
			pr, pw := io.Pipe()
			return pw, pr
		}, "end\n"},
		{"Pseudo", func(t *testing.T) (io.Writer, io.Reader) {
			primary, secondary, err := pty.Open()
			if err != nil {
				t.Fatal(err)
			}
			return secondary, primary
		}, "012end\r\n"},
	}
	file := parse(t, nil, `
		for n in 0 1 2 3; do if [[ -t $n ]]; then echo -n $n; fi; done; echo end
	`)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			secondary, primary := test.files(t)
			// some secondary ends can be used as stdin too
			secondaryReader, _ := secondary.(io.Reader)

			r, _ := interp.New(interp.StdIO(secondaryReader, secondary, secondary))
			go func() {
				// To mimic [os/exec.Cmd.Start], use a goroutine.
				if err := r.Run(context.Background(), file); err != nil {
					t.Error(err)
				}
			}()

			got, err := bufio.NewReader(primary).ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("\nwant: %q\ngot:  %q", test.want, got)
			}
			if closer, ok := secondary.(io.Closer); ok {
				if err := closer.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if closer, ok := primary.(io.Closer); ok {
				if err := closer.Close(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestRunnerTerminalExec(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		start func(*testing.T, *exec.Cmd) io.Reader
		want  string
	}{
		{"Nil", func(t *testing.T, cmd *exec.Cmd) io.Reader {
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			return strings.NewReader("\n")
		}, "\n"},
		{"Pipe", func(t *testing.T, cmd *exec.Cmd) io.Reader {
			out, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			cmd.Stderr = cmd.Stdout
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			return out
		}, "end\n"},
		{"Pseudo", func(t *testing.T, cmd *exec.Cmd) io.Reader {
			// Note that we avoid pty.Start,
			// as it closes the secondary terminal via a defer,
			// possibly before the command has finished.
			// That can lead to "signal: hangup" flakes.
			primary, secondary, err := pty.Open()
			if err != nil {
				t.Fatal(err)
			}
			cmd.Stdin = secondary
			cmd.Stdout = secondary
			cmd.Stderr = secondary
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			return primary
		}, "012end\r\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			cmd := exec.Command(os.Getenv("GOSH_PROG"),
				"for n in 0 1 2 3; do if [[ -t $n ]]; then echo -n $n; fi; done; echo end")
			primary := test.start(t, cmd)

			got, err := bufio.NewReader(primary).ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("\nwant: %q\ngot:  %q", test.want, got)
			}
			if closer, ok := primary.(io.Closer); ok {
				if err := closer.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if err := cmd.Wait(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestExecETXTBSY(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "script.sh")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("#!/bin/sh\necho foo\n"); err != nil {
		t.Fatal(err)
	}
	// Hand the write fd to a child process which outlives our close below,
	// mimicking a concurrent fork inheriting the fd before its exec;
	// see https://go.dev/issue/22315. Executing the script fails with ETXTBSY
	// until the child exits, which happens once we close its stdin.
	holder := exec.Command("cat")
	holderStdin, err := holder.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	holder.ExtraFiles = []*os.File{f}
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	defer holder.Wait()
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	time.AfterFunc(100*time.Millisecond, func() { holderStdin.Close() })
	var buf bytes.Buffer
	r, err := interp.New(interp.StdIO(nil, &buf, &buf))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), parse(t, nil, path)); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "foo\n" {
		t.Fatalf("want %q, got %q", "foo\n", got)
	}
}

type fifoPathWriter chan string

func (w fifoPathWriter) Write(data []byte) (int, error) {
	w <- string(data)
	return len(data), nil
}

func TestCancelUnusedProcessSubstitution(t *testing.T) {
	t.Parallel()
	for _, script := range []string{
		`printf '%s\n' <(printf hello); wait`,
		`printf '%s\n' >(cat); wait`,
	} {
		t.Run(script, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			ready := make(fifoPathWriter, 1)
			runner, err := interp.New(interp.Dir(t.TempDir()), interp.StdIO(nil, ready, nil))
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			program := parse(t, nil, script)
			go func() { done <- runner.Run(ctx, program) }()
			var path string
			select {
			case path = <-ready:
				path = strings.TrimSpace(path)
			case <-time.After(5 * time.Second):
				t.Fatal("process substitution did not start")
			}
			defer os.Remove(path)
			cancel()
			select {
			case err := <-done:
				if err != nil && !errors.Is(err, context.Canceled) {
					t.Fatalf("Run returned %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("process substitution did not stop")
			}
			// Cancellation must release the FIFO as well as the execution handle.
			deadline := time.Now().Add(5 * time.Second)
			for {
				_, err := os.Stat(path)
				if errors.Is(err, os.ErrNotExist) {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("process substitution left its FIFO open")
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
}
