// Package xz implements xz compression and decompression.
package xz

import (
	"bytes"
	"fmt"
	"github.com/jamespfennell/xz/lzma"
	"io"
)

const (
	BestSpeed          = 0
	BestCompression    = 9
	DefaultCompression = 6
)

// LzmaError may be returned if the underlying lzma library returns an error code during compression or decompression.
// Receiving this error indicates a bug in the xz package, and a bug report would be appreciated.
type LzmaError struct {
	result lzma.Return
}

func (err LzmaError) Error() string {
	return fmt.Sprintf(
		"lzma library returned a %s error. This indicates a bug in the Go xz package", err.result)
}

// Writer is an io.WriteCloser that xz-compresses its input and writes it to an underlying io.Writer
type Writer struct {
	lzmaStream *lzma.Stream
	w          io.Writer
	lastErr    error
}

// NewWriter creates a Writer that compresses with the default compression level of DefaultCompression and writes the
// output to w.
func NewWriter(w io.Writer) *Writer {
	return NewWriterLevel(w, DefaultCompression)
}

// NewWriterLevel creates a Writer that compresses with the prescribed compression level and writes the output to w.
// The level should be between BestSpeed and BestCompression inclusive; if it isn't, the level will be rounded up
// or down accordingly.
func NewWriterLevel(w io.Writer, level int) *Writer {
	if level < BestSpeed {
		fmt.Printf("xz library: unexpected negative compression level %d; using level 0\n", level)
		level = BestSpeed
	}
	if level > BestCompression {
		fmt.Printf("xz library: unexpected compression level %d bigger than 9; using level 9\n", level)
		level = BestCompression
	}
	s := lzma.NewStream()
	if ret := lzma.EasyEncoder(s, level); ret != lzma.Ok {
		fmt.Printf("xz library: unexpected result from encoder initialization: %s\n", ret)
	}
	return &Writer{
		lzmaStream: s,
		w:          w,
	}
}

// Write accepts p for compression.
//
// Because of internal buffering and the mechanics of xz, the compressed version of p is not guaranteed to have been
// written to the underlying io.Writer when the function returns.
func (z *Writer) Write(p []byte) (int, error) {
	z.lzmaStream.SetInput(p)
	start := z.lzmaStream.TotalIn()
	err := runLzma(z.lzmaStream, z.w, false)
	return z.lzmaStream.TotalIn() - start, err
}

// Close finishes processing any input that has yet to be compressed, writes all remaining output to the underlying
// io.Writer, and frees memory resources associated to the Writer.
func (z *Writer) Close() error {
	err := runLzma(z.lzmaStream, z.w, true)
	z.lzmaStream.Close()
	return err
}

// Reader is an io.ReadCloser that xz-decompresses from an underlying io.Reader.
type Reader struct {
	lzmaStream    *lzma.Stream
	r             io.Reader
	buf           bytes.Buffer
	inputFinished bool
	lastErr       error
}

// NewReader creates a new Reader that reads compressed input from r.
func NewReader(r io.Reader) *Reader {
	s := lzma.NewStream()
	if ret := lzma.StreamDecoder(s); ret != lzma.Ok {
		fmt.Printf("xz library: unexpected result from decoder initialization: %s\n", ret)
	}
	return &Reader{
		lzmaStream: s,
		r:          r,
	}
}

// Read decompresses output from the underlying io.Reader and returns up to len(p) uncompressed bytes.
func (z *Reader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if z.lastErr != nil {
		return 0, z.lastErr
	}
	if z.buf.Len() < len(p) {
		// We have no idea how much data to request from the underlying io.Reader, so just cargo cult from the caller...
		z.lastErr = z.populateBuffer(len(p))
		if z.lastErr != nil {
			return 0, z.lastErr
		}
	}
	var n int
	n, z.lastErr = z.buf.Read(p)
	return n, z.lastErr
}

func (z *Reader) populateBuffer(sizeHint int) error {
	if z.inputFinished {
		return nil
	}

	q := make([]byte, sizeHint)
	m, err := z.r.Read(q)
	if err != nil && err != io.EOF {
		return err
	}
	if err == io.EOF {
		z.inputFinished = true
	}
	z.lzmaStream.SetInput(q[:m])

	return runLzma(z.lzmaStream, &z.buf, z.inputFinished)
}

// Close released resources associated to this Reader.
func (z *Reader) Close() error {
	z.lzmaStream.Close()
	return nil
}

func runLzma(lzmaStream *lzma.Stream, w io.Writer, finish bool) error {
	action := lzma.Run
	for {
		// When decoding with lzma.Run, lzma requires the input buffer be non-empty. So if it is empty, either return
		// or transition to lzma.Finish.
		if action == lzma.Run && lzmaStream.AvailIn() == 0 {
			if !finish {
				break
			}
			action = lzma.Finish
		}
		result := lzma.Code(lzmaStream, action)
		// The output buffer is not necessarily full, but for simplicity we just copy and clear it.
		// An alternative would be to remove the write here and replace it with the following 2 writes:
		//   1. before lzma.Code if lzmaStream.AvailOut() == 0; i.e., clear the buffer if we're out of space.
		//   2. before the function returns at the end, so the last output is captured.
		if _, err := w.Write(lzmaStream.Output()); err != nil {
			return err
		}
		if action == lzma.Finish && result == lzma.StreamEnd {
			break
		}
		if result.IsErr() {
			return LzmaError{result: result}
		}
	}
	return nil
}
