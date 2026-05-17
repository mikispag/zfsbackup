package zfs

import (
	"io"
	"strings"
	"testing"
	"time"
)

func TestParseDuration_valid(t *testing.T) {
	tests := []struct {
		input string
		want  time.Duration
	}{
		{"1s", time.Second},
		{"5m", 5 * time.Minute},
		{"2h", 2 * time.Hour},
		{"30d", 30 * 24 * time.Hour},
		{"1w", 7 * 24 * time.Hour},
		{"1y", 365 * 24 * time.Hour},
		{"365d", 365 * 24 * time.Hour},
		{"0s", 0},
		{"100d", 100 * 24 * time.Hour},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got, err := ParseDuration(tc.input)
			if err != nil {
				t.Fatalf("ParseDuration(%q) error = %v; want nil", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("ParseDuration(%q) = %v; want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestParseDuration_invalid(t *testing.T) {
	cases := []string{
		"",
		"x",
		"30",
		"30x",
		"1",
		"d",
		"h",
		"1.5d",                 // non-integer prefix: ParseInt fails
		"9999999999999999999d", // int64 overflow: ParseInt fails
	}
	for _, input := range cases {
		t.Run(input, func(t *testing.T) {
			_, err := ParseDuration(input)
			if err == nil {
				t.Errorf("ParseDuration(%q) expected error, got nil", input)
			}
		})
	}
}

func TestCompressionProg_none(t *testing.T) {
	if got := CompressionProg("none"); got != "" {
		t.Errorf(`CompressionProg("none") = %q; want ""`, got)
	}
}

func TestCompressionProg_noneUpperCase(t *testing.T) {
	if got := CompressionProg("NONE"); got != "" {
		t.Errorf(`CompressionProg("NONE") = %q; want ""`, got)
	}
}

func TestCompressionProg_zstd(t *testing.T) {
	if got := CompressionProg("zstd"); got != "zstd" {
		t.Errorf(`CompressionProg("zstd") = %q; want "zstd"`, got)
	}
}

func TestCompressionProg_zstdUpperCase(t *testing.T) {
	if got := CompressionProg("ZSTD"); got != "zstd" {
		t.Errorf(`CompressionProg("ZSTD") = %q; want "zstd"`, got)
	}
}

// TestMaybeMbuffer_noArgs verifies that MaybeMbuffer with empty args returns the
// original reader unchanged and no command.
func TestMaybeMbuffer_noArgs_returnsInputUnchanged(t *testing.T) {
	original := io.NopCloser(strings.NewReader("hello"))
	got, cmd := MaybeMbuffer(nil, nil, original)
	if got != original {
		t.Error("MaybeMbuffer(nil, nil, input) should return input unchanged")
	}
	if cmd != nil {
		t.Error("MaybeMbuffer(nil, nil, input) should return nil cmd")
	}
}

func TestParseTabular_commandFails_returnsError(t *testing.T) {
	// "false" is a standard Unix command that always exits with code 1.
	_, err := ParseTabular("false", nil)
	if err == nil {
		t.Error("expected error when command exits non-zero, got nil")
	}
}

func TestMaybeMbuffer_emptySlice_returnsInputUnchanged(t *testing.T) {
	original := io.NopCloser(strings.NewReader("data"))
	got, cmd := MaybeMbuffer(nil, []string{}, original)
	if got != original {
		t.Error("MaybeMbuffer with empty slice should return input unchanged")
	}
	if cmd != nil {
		t.Error("MaybeMbuffer with empty slice should return nil cmd")
	}
}
