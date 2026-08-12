// Package pktline implements git's pkt-line framing, which is the wire format
// of the long running filter protocol.
//
// A packet is a four byte hexadecimal length, counting the length bytes
// themselves, followed by the payload. The special length "0000" is a flush
// packet and terminates a section of the stream.
package pktline

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// MaxPayload is the largest payload a single packet may carry.
const MaxPayload = 65516

// ErrFlush is returned by ReadPacket when a flush packet is read.
var ErrFlush = errors.New("flush packet")

// Reader reads pkt-line framed data.
type Reader struct {
	r   *bufio.Reader
	buf []byte
}

// NewReader wraps r.
func NewReader(r io.Reader) *Reader {
	return &Reader{r: bufio.NewReaderSize(r, MaxPayload+4), buf: make([]byte, MaxPayload)}
}

// ReadPacket reads one packet. The returned slice is only valid until the
// next read. A flush packet yields ErrFlush.
func (r *Reader) ReadPacket() ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(r.r, header[:]); err != nil {
		return nil, err
	}
	n, err := strconv.ParseUint(string(header[:]), 16, 32)
	if err != nil {
		return nil, fmt.Errorf("invalid pkt-line length %q: %w", header, err)
	}
	switch {
	case n == 0:
		return nil, ErrFlush
	case n < 4:
		return nil, fmt.Errorf("invalid pkt-line length %d", n)
	case n-4 > MaxPayload:
		return nil, fmt.Errorf("pkt-line payload of %d bytes is too large", n-4)
	}
	payload := r.buf[:n-4]
	if _, err := io.ReadFull(r.r, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

// ReadPacketText reads one packet as a line, with any trailing newline
// removed.
func (r *Reader) ReadPacketText() (string, error) {
	p, err := r.ReadPacket()
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(string(p), "\n"), nil
}

// ReadPacketList reads text packets until a flush packet.
func (r *Reader) ReadPacketList() ([]string, error) {
	var out []string
	for {
		s, err := r.ReadPacketText()
		if errors.Is(err, ErrFlush) {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		out = append(out, s)
	}
}

// CopyTo streams packet payloads into w until a flush packet.
func (r *Reader) CopyTo(w io.Writer) error {
	for {
		p, err := r.ReadPacket()
		if errors.Is(err, ErrFlush) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := w.Write(p); err != nil {
			return err
		}
	}
}

// Discard reads and throws away packets until a flush packet.
func (r *Reader) Discard() error { return r.CopyTo(io.Discard) }

// Writer writes pkt-line framed data.
type Writer struct {
	w *bufio.Writer
}

// NewWriter wraps w.
func NewWriter(w io.Writer) *Writer {
	return &Writer{w: bufio.NewWriterSize(w, MaxPayload+4)}
}

// WritePacket writes one packet. The payload must not exceed MaxPayload.
func (w *Writer) WritePacket(p []byte) error {
	if len(p) > MaxPayload {
		return fmt.Errorf("payload of %d bytes exceeds the pkt-line maximum", len(p))
	}
	if _, err := fmt.Fprintf(w.w, "%04x", len(p)+4); err != nil {
		return err
	}
	_, err := w.w.Write(p)
	return err
}

// WritePacketText writes a text packet, appending a newline as git expects.
func (w *Writer) WritePacketText(s string) error {
	return w.WritePacket([]byte(s + "\n"))
}

// WriteFlush writes a flush packet.
func (w *Writer) WriteFlush() error {
	_, err := w.w.WriteString("0000")
	return err
}

// CopyFrom streams r into packets, splitting it as needed. It does not write
// a trailing flush packet.
func (w *Writer) CopyFrom(r io.Reader) error {
	buf := make([]byte, MaxPayload)
	for {
		n, err := io.ReadFull(r, buf)
		if n > 0 {
			if werr := w.WritePacket(buf[:n]); werr != nil {
				return werr
			}
		}
		switch {
		case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
			return nil
		case err != nil:
			return err
		}
	}
}

// Flush flushes the underlying buffered writer.
func (w *Writer) Flush() error { return w.w.Flush() }
