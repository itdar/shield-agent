package process

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"rua/internal/middleware"
	"rua/internal/monitor"
)

// RunWithMiddleware launches the child process described by args and wires up
// PipelineIn/PipelineOut interceptors that run the provided middleware chain.
// If chain is nil it falls back to the plain Interceptor.
// monSrv and metrics may be nil.
func RunWithMiddleware(ctx context.Context, args []string, logger *slog.Logger, chain *middleware.Chain, metrics *monitor.Metrics, monSrv *monitor.Server) error {
	if len(args) == 0 {
		return errors.New("no command specified")
	}

	bin, err := exec.LookPath(args[0])
	if err != nil {
		return fmt.Errorf("command not found: %q", args[0])
	}

	cmd := exec.CommandContext(ctx, bin, args[1:]...)
	cmd.Stderr = os.Stderr

	childStdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("creating stdin pipe: %w", err)
	}

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

	if monSrv != nil {
		monSrv.SetChildPID(cmd.Process.Pid)
	}
	if metrics != nil {
		metrics.ChildProcessUp.Set(1.0)
	}

	obs := make(chan []byte, 256)
	go drainObserver(obs, logger, "pipeline")

	stdinDone := make(chan error, 1)
	stdoutDone := make(chan error, 1)

	if chain != nil {
		obs2 := make(chan []byte, 256)
		go drainObserver(obs2, logger, "pipeline-out")

		go func() {
			err := PipelineIn(os.Stdin, childStdin, os.Stdout, chain, logger, obs)
			childStdin.Close()
			stdinDone <- err
		}()
		go func() {
			err := PipelineOut(childStdout, os.Stdout, chain, logger, obs2)
			stdoutDone <- err
		}()
	} else {
		go func() {
			err := Interceptor(os.Stdin, childStdin, obs)
			childStdin.Close()
			stdinDone <- err
		}()
		go func() {
			obs2 := make(chan []byte, 256)
			go drainObserver(obs2, logger, "stdout")
			err := Interceptor(childStdout, os.Stdout, obs2)
			stdoutDone <- err
		}()
	}

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

	waitErr := cmd.Wait()

	signal.Stop(sigCh)
	close(sigCh)

	waitForDone(stdinDone)
	waitForDone(stdoutDone)

	if metrics != nil {
		metrics.ChildProcessUp.Set(0.0)
	}

	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			code := exitErr.ExitCode()
			logger.Info("child process exited",
				slog.Int("pid", cmd.Process.Pid),
				slog.Int("exit_code", code),
			)
			return exitErr
		}
		logger.Error("child process error", slog.String("error", waitErr.Error()))
		return fmt.Errorf("child process error: %w", waitErr)
	}

	logger.Info("child process exited cleanly", slog.Int("pid", cmd.Process.Pid))
	return nil
}
