package config

import "testing"

// --- ResolveInclude ---

func TestResolveInclude_moduleOverridesTop(t *testing.T) {
	c := &Config{Include: []string{"tank"}}
	got := c.ResolveInclude([]string{"backup"})
	if len(got) != 1 || got[0] != "backup" {
		t.Errorf("got %v; want [backup]", got)
	}
}

func TestResolveInclude_emptyModuleUsesTop(t *testing.T) {
	c := &Config{Include: []string{"tank"}}
	got := c.ResolveInclude(nil)
	if len(got) != 1 || got[0] != "tank" {
		t.Errorf("got %v; want [tank]", got)
	}
}

func TestResolveInclude_emptySliceModuleUsesTop(t *testing.T) {
	c := &Config{Include: []string{"tank"}}
	got := c.ResolveInclude([]string{})
	if len(got) != 1 || got[0] != "tank" {
		t.Errorf("got %v; want [tank]", got)
	}
}

func TestResolveInclude_bothEmpty_returnsNil(t *testing.T) {
	c := &Config{}
	got := c.ResolveInclude(nil)
	if len(got) != 0 {
		t.Errorf("got %v; want empty", got)
	}
}

// --- ResolveExclude ---

func TestResolveExclude_moduleOverridesTop(t *testing.T) {
	c := &Config{Exclude: []string{"tank/scratch"}}
	got := c.ResolveExclude([]string{"tank/tmp"})
	if len(got) != 1 || got[0] != "tank/tmp" {
		t.Errorf("got %v; want [tank/tmp]", got)
	}
}

func TestResolveExclude_emptyModuleUsesTop(t *testing.T) {
	c := &Config{Exclude: []string{"tank/scratch"}}
	got := c.ResolveExclude(nil)
	if len(got) != 1 || got[0] != "tank/scratch" {
		t.Errorf("got %v; want [tank/scratch]", got)
	}
}

func TestResolveExclude_bothEmpty_returnsNil(t *testing.T) {
	c := &Config{}
	got := c.ResolveExclude(nil)
	if len(got) != 0 {
		t.Errorf("got %v; want empty", got)
	}
}

func TestResolveExclude_multipleEntries(t *testing.T) {
	c := &Config{Exclude: []string{"tank/a", "tank/b"}}
	got := c.ResolveExclude(nil)
	if len(got) != 2 {
		t.Errorf("got %v; want 2 entries", got)
	}
}
