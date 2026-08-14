package pktline

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestWriteFraming(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	if err := w.WritePacketText("version=2"); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteFlush(); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	// 4 length bytes + "version=2\n" is 14 bytes, 000e in hex.
	if got, want := buf.String(), "000eversion=2\n0000"; got != want {
		t.Errorf("framing = %q, want %q", got, want)
	}
}

func TestReadPacketList(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	for _, s := range []string{"git-filter-client", "version=2"} {
		if err := w.WritePacketText(s); err != nil {
			t.Fatal(err)
		}
	}
	w.WriteFlush()
	w.Flush()

	got, err := NewReader(&buf).ReadPacketList()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"git-filter-client", "version=2"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestEmptyListIsFlushOnly(t *testing.T) {
	got, err := NewReader(strings.NewReader("0000")).ReadPacketList()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want an empty list", got)
	}
}

func TestContentRoundTripAcrossPackets(t *testing.T) {
	// Content larger than one packet has to be split and rejoined exactly.
	content := bytes.Repeat([]byte("git-s3fs "), MaxPayload/4)

	var wire bytes.Buffer
	w := NewWriter(&wire)
	if err := w.CopyFrom(bytes.NewReader(content)); err != nil {
		t.Fatal(err)
	}
	w.WriteFlush()
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}

	var got bytes.Buffer
	if err := NewReader(&wire).CopyTo(&got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Bytes(), content) {
		t.Errorf("content round trip failed: got %d bytes, want %d", got.Len(), len(content))
	}
}

func TestReadPacketFlush(t *testing.T) {
	_, err := NewReader(strings.NewReader("0000")).ReadPacket()
	if !errors.Is(err, ErrFlush) {
		t.Errorf("err = %v, want ErrFlush", err)
	}
}

func TestReadPacketRejectsGarbage(t *testing.T) {
	for _, in := range []string{"zzzz", "0001", "0003"} {
		if _, err := NewReader(strings.NewReader(in)).ReadPacket(); err == nil || errors.Is(err, ErrFlush) {
			t.Errorf("ReadPacket(%q) err = %v, want a parse failure", in, err)
		}
	}
}

func TestReadPacketTruncated(t *testing.T) {
	if _, err := NewReader(strings.NewReader("0010ab")).ReadPacket(); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("err = %v, want io.ErrUnexpectedEOF", err)
	}
}

func TestWritePacketTooLarge(t *testing.T) {
	w := NewWriter(io.Discard)
	if err := w.WritePacket(make([]byte, MaxPayload+1)); err == nil {
		t.Error("oversized packets should be rejected")
	}
}

func TestCopyFromEmpty(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	if err := w.CopyFrom(strings.NewReader("")); err != nil {
		t.Fatal(err)
	}
	w.Flush()
	if buf.Len() != 0 {
		t.Errorf("an empty stream should emit no packets, got %q", buf.String())
	}
}
