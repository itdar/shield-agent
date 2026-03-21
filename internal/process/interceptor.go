package process

import (
	"io"
)

// Interceptor copies bytes from src to dst while also sending each chunk to
// the out channel for observation. The channel receives raw byte slices as
// they are read; each slice is a fresh copy safe for concurrent use.
//
// Interceptor runs until src returns io.EOF or any other read error.
// It closes the out channel when it finishes.
func Interceptor(src io.Reader, dst io.Writer, out chan<- []byte) error {
	defer close(out)

	buf := make([]byte, 32*1024)
	for {
		n, readErr := src.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])

			// Send to observer (non-blocking drop if channel is full).
			select {
			case out <- chunk:
			default:
			}

			if _, writeErr := dst.Write(chunk); writeErr != nil {
				return writeErr
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				return nil
			}
			return readErr
		}
	}
}
