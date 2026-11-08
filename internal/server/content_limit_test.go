package server

import (
	"errors"
	"testing"
)

func TestCheckContent(t *testing.T) {
	s := &Server{maxContentBytes: 10}
	if err := s.checkContent("short"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	err := s.checkContent("0123456789A") // 11 bytes
	if err == nil {
		t.Fatal("expected a too-large error")
	}
	var cte *ContentTooLargeError
	if !errors.As(err, &cte) || cte.Size != 11 || cte.Max != 10 {
		t.Errorf("unexpected error: %#v", err)
	}
	// A disabled cap accepts anything.
	s = &Server{maxContentBytes: 0}
	if err := s.checkContent("0123456789A"); err != nil {
		t.Errorf("unbounded cap should accept: %v", err)
	}
}

func TestWithMaxContentBytes(t *testing.T) {
	s := NewServer(nil, WithMaxContentBytes(42))
	if s.maxContentBytes != 42 {
		t.Errorf("maxContentBytes = %d, want 42", s.maxContentBytes)
	}
	// A non-positive option must not clobber the default.
	s = NewServer(nil, WithMaxContentBytes(0))
	if s.maxContentBytes != DefaultMaxContentBytes {
		t.Errorf("maxContentBytes = %d, want default %d", s.maxContentBytes, DefaultMaxContentBytes)
	}
}
