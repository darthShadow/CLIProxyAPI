package test

import (
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

// TestRelease1CatalogGPT61SolEffortsLowToMax is the catalog part of recovered
// row 116 (fixture F25): the embedded Codex Pro catalog that P and W load with
// -local-model offers gpt-6.1-sol with efforts low to max and no ultra level.
func TestRelease1CatalogGPT61SolEffortsLowToMax(t *testing.T) {
	info := registry.LookupStaticModelInfoByChannel("gpt-6.1-sol", "codex")
	if info == nil || info.Thinking == nil {
		t.Fatalf("gpt-6.1-sol missing from the embedded codex catalog: %+v", info)
	}
	want := []string{"low", "medium", "high", "xhigh", "max"}
	if !reflect.DeepEqual(info.Thinking.Levels, want) {
		t.Fatalf("levels = %v, want %v (no ultra)", info.Thinking.Levels, want)
	}
}
