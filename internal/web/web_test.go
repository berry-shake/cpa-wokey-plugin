package web

import (
	"bytes"
	"testing"
)

func TestPage(t *testing.T) {
	if page := Page(); !bytes.Contains(page, []byte("<html")) {
		t.Fatalf("page is not HTML: %.100q", page)
	}
}

func TestLogo(t *testing.T) {
	if !bytes.Contains(Logo(), []byte("<svg")) {
		t.Fatal("logo is not an SVG")
	}
}
