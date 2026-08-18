package tui

import (
	"reflect"
	"testing"
)

func TestParseEngineEnvironment(t *testing.T) {
	got, err := parseEngineEnvironment("FLASHINFER_EXTRA_CUDAFLAGS=-allow-unsupported-compiler; CUDA_VISIBLE_DEVICES=0,1")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"FLASHINFER_EXTRA_CUDAFLAGS": "-allow-unsupported-compiler",
		"CUDA_VISIBLE_DEVICES":       "0,1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestParseEngineEnvironmentRejectsInvalidAssignment(t *testing.T) {
	if _, err := parseEngineEnvironment("NOT_AN_ASSIGNMENT"); err == nil {
		t.Fatal("expected validation error")
	}
}
