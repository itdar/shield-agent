package process

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"
)

const gracefulShutdownTimeout = 5 * time.Second

// Run launches the program described by args (args[0] is the executable,
// args[1:] are its arguments), wires up stdin/stdout interceptors, and
// forwards signals. It blocks until the child process exits and returns any
// error. The exit code of the child is propagated via *exec.ExitError.
func Run(ctx context.Context, args []string, logger *slog.Logger) error {
	if len(args) == 0 {
		return errors.New("no command specified")
	}

	// Resolve the binary to give a clear error early.
	bin, err := exec.LookPath(args[0])
	if err != nil {
		return fmt.Errorf("command not found: %q", args[0])
	}

	cmd := exec.CommandContext(ctx, bin, args[1:]...)

	// stderr passes through directly — no interception.
	cmd.Stderr = os.Stderr

	// Set up stdin pipe: os.Stdin → interceptor → child stdin.
	childStdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("creating stdin pipe: %w", err)
	}

	// Set up stdout pipe: child stdout → interceptor → os.Stdout.
	childStdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("creating stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting process: %w", err)
	}

	logger.Info("child process started",
		slog.String("command", args[0]),
		slog.Int("pid", cmd.Process.Pid),
	)

	// Channels that collect intercepted bytes (buffered to avoid blocking).
	stdinObs := make(chan []byte, 256)
	stdoutObs := make(chan []byte, 256)

	// Drain observer channels so interceptors never block on send.
	go drainObserver(stdinObs, logger, "stdin")
	go drainObserver(stdoutObs, logger, "stdout")

	// Wire interceptors.
	stdinDone := make(chan error, 1)
	stdoutDone := make(chan error, 1)

	go func() {
		err := Interceptor(os.Stdin, childStdin, stdinObs)
		childStdin.Close()
		stdinDone <- err
	}()

	go func() {
		err := Interceptor(childStdout, os.Stdout, stdoutObs)
		stdoutDone <- err
	}()

	// Forward OS signals to the child.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		for sig := range sigCh {
			logger.Info("forwarding signal to child", slog.String("signal", sig.String()))
			if cmd.Process != nil {
				_ = cmd.Process.Signal(sig)
			}
		}
	}()

	// Wait for the child to exit.
	waitErr := cmd.Wait()

	signal.Stop(sigCh)
	close(sigCh)

	// Drain interceptor goroutines.
	waitForDone(stdinDone)
	waitForDone(stdoutDone)

	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			code := exitErr.ExitCode()
			logger.Info("child process exited",
				slog.Int("pid", cmd.Process.Pid),
				slog.Int("exit_code", code),
			)
			// Propagate the child's exit code to the caller.
			return exitErr
		}
		logger.Error("child process error",
			slog.String("error", waitErr.Error()),
		)
		return fmt.Errorf("child process error: %w", waitErr)
	}

	logger.Info("child process exited cleanly", slog.Int("pid", cmd.Process.Pid))
	return nil
}

// kill sends SIGKILL to the process after the graceful shutdown timeout.
func kill(proc *os.Process, logger *slog.Logger) {
	timer := time.NewTimer(gracefulShutdownTimeout)
	defer timer.Stop()
	<-timer.C
	logger.Warn("graceful shutdown timeout — sending SIGKILL",
		slog.Int("pid", proc.Pid),
	)
	_ = proc.Signal(syscall.SIGKILL)
}

// waitForDone drains a done channel, ignoring io.EOF and broken-pipe errors
// which are normal at shutdown.
func waitForDone(ch <-chan error) {
	if err := <-ch; err != nil && !isBrokenPipe(err) && err != io.EOF {
		_ = err // interceptor errors are best-effort
	}
}

// isBrokenPipe returns true for broken-pipe / write-on-closed-pipe errors.
func isBrokenPipe(err error) bool {
	if err == nil {
		return false
	}
	var sysErr *os.PathError
	if errors.As(err, &sysErr) {
		if errno, ok := sysErr.Err.(syscall.Errno); ok {
			return errno == syscall.EPIPE
		}
	}
	return false
}

// drainObserver reads from ch until it is closed, logging each chunk at debug
// level so upstream observers do not block the pipe.
func drainObserver(ch <-chan []byte, logger *slog.Logger, direction string) {
	for chunk := range ch {
		logger.Debug("intercepted bytes",
			slog.String("direction", direction),
			slog.Int("bytes", len(chunk)),
		)
	}
}
