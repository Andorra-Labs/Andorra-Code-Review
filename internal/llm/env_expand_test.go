package llm

import (
	"strings"
	"testing"
)

func TestExpandEnvPlaceholdersNoMatch(t *testing.T) {
	got, err := ExpandEnvPlaceholders("sk-literal-key")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "sk-literal-key" {
		t.Errorf("got %q, want unchanged", got)
	}
}

func TestExpandEnvPlaceholdersReplacesSet(t *testing.T) {
	t.Setenv("OCR_TEST_KEY", "secret-value")
	got, err := ExpandEnvPlaceholders("${env:OCR_TEST_KEY}")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "secret-value" {
		t.Errorf("got %q", got)
	}
}

func TestExpandEnvPlaceholdersErrorsOnUnset(t *testing.T) {
	_, err := ExpandEnvPlaceholders("${env:OCR_DEFINITELY_UNSET_FOR_TEST_XYZ}")
	if err == nil {
		t.Fatal("expected error for unset env var")
	}
	if !strings.Contains(err.Error(), "OCR_DEFINITELY_UNSET_FOR_TEST_XYZ") {
		t.Errorf("err missing var name: %v", err)
	}
}

func TestExpandEnvPlaceholdersMultiple(t *testing.T) {
	t.Setenv("OCR_A", "alpha")
	t.Setenv("OCR_B", "beta")
	got, err := ExpandEnvPlaceholders("prefix:${env:OCR_A}-${env:OCR_B}:suffix")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "prefix:alpha-beta:suffix" {
		t.Errorf("got %q", got)
	}
}

func TestExpandEnvPlaceholdersInvalidNameIgnored(t *testing.T) {
	// lowercase names don't match the placeholder pattern
	got, err := ExpandEnvPlaceholders("${env:lowercase}")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "${env:lowercase}" {
		t.Errorf("got %q, want unchanged for non-matching pattern", got)
	}
}
