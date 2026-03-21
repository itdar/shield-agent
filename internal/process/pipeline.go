package process

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"

	"rua/internal/jsonrpc"
	"rua/internal/middleware"
)

// PipelineIn processes the parent→child direction (stdin).
// Parsed JSON-RPC requests are passed through the middleware chain.
// If a request is blocked, a JSON-RPC error response is written to errWriter
// (the upstream stdout) instead of forwarding to the child.
// Non-JSON or non-request messages are forwarded verbatim.
func PipelineIn(src io.Reader, dst io.Writer, errWriter io.Writer, chain *middleware.Chain, logger *slog.Logger, out chan<- []byte) error {
	parser := jsonrpc.NewParser(src, jsonrpc.ModeAuto)
	ctx := context.Background()

	for {
		msg, err := parser.Next()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}

		// Always forward raw to observer (non-blocking).
		select {
		case out <- msg.Raw:
		default:
		}

		if !msg.IsJSON || msg.Request == nil {
			// Non-JSON or a response in the inbound direction — forward as-is.
			if _, werr := dst.Write(append(msg.Raw, '\n')); werr != nil {
				return werr
			}
			continue
		}

		// Run through middleware chain.
		modified, errPayload, chainErr := chain.ProcessRequest(ctx, msg.Request)
		if chainErr != nil {
			// Write error response back to upstream (not to child).
			if errWriter != nil && len(errPayload) > 0 {
				_, _ = errWriter.Write(errPayload)
			}
			logger.Warn("request blocked by middleware",
				slog.String("method", msg.Request.Method),
				slog.String("error", chainErr.Error()),
			)
			continue
		}

		// Marshal the (possibly modified) request and forward to child.
		out2, merr := json.Marshal(modified)
		if merr != nil {
			logger.Error("failed to marshal modified request", slog.String("error", merr.Error()))
			// Fall back to original raw.
			out2 = msg.Raw
		}
		if _, werr := dst.Write(append(out2, '\n')); werr != nil {
			return werr
		}
	}
}

// PipelineOut processes the child→parent direction (stdout).
// Parsed JSON-RPC responses are passed through the middleware chain.
// Non-JSON or non-response messages are forwarded verbatim.
func PipelineOut(src io.Reader, dst io.Writer, chain *middleware.Chain, logger *slog.Logger, out chan<- []byte) error {
	parser := jsonrpc.NewParser(src, jsonrpc.ModeAuto)
	ctx := context.Background()

	for {
		msg, err := parser.Next()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}

		// Forward to observer (non-blocking).
		select {
		case out <- msg.Raw:
		default:
		}

		if !msg.IsJSON || msg.Resp == nil {
			// Not a response (could be a server-initiated request/notification).
			if _, werr := dst.Write(append(msg.Raw, '\n')); werr != nil {
				return werr
			}
			continue
		}

		modified, chainErr := chain.ProcessResponse(ctx, msg.Resp)
		if chainErr != nil {
			logger.Warn("response blocked by middleware",
				slog.String("error", chainErr.Error()),
			)
			// Drop the response — don't forward.
			continue
		}

		out2, merr := json.Marshal(modified)
		if merr != nil {
			logger.Error("failed to marshal modified response", slog.String("error", merr.Error()))
			out2 = msg.Raw
		}
		if _, werr := dst.Write(append(out2, '\n')); werr != nil {
			return werr
		}
	}
}
