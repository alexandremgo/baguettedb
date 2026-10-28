package domain

import (
	"bytes"
	"testing"
)

func TestGenerateDEK_Length(t *testing.T) {
	dek, err := GenerateDEK()
	if err != nil {
		t.Fatalf("GenerateDEK: %v", err)
	}
	if len(dek) != DEKSize {
		t.Fatalf("expected %d bytes, got %d", DEKSize, len(dek))
	}
}

func TestGenerateDEK_NonRepeating(t *testing.T) {
	a, err := GenerateDEK()
	if err != nil {
		t.Fatalf("GenerateDEK a: %v", err)
	}
	b, err := GenerateDEK()
	if err != nil {
		t.Fatalf("GenerateDEK b: %v", err)
	}
	if bytes.Equal(a, b) {
		t.Fatal("two consecutive DEKs are identical — sanity check failed")
	}
}
