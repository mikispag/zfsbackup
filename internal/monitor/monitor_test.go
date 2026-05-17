package monitor

import (
	"strings"
	"testing"
	"time"

	"github.com/mikispag/zfsbackup/internal/config"
)

// --- metric.asPrometheus ---

func TestMetricAsPrometheus_noDimensions(t *testing.T) {
	m := metric{
		Name:          "MyMetric",
		Dimensions:    map[string]string{},
		Value:         42,
		EvalTimestamp: time.Now(),
	}
	got := m.asPrometheus()
	want := "MyMetric 42"
	if got != want {
		t.Errorf("asPrometheus() = %q; want %q", got, want)
	}
}

func TestMetricAsPrometheus_nilDimensions(t *testing.T) {
	m := metric{
		Name:  "MyMetric",
		Value: 0,
	}
	got := m.asPrometheus()
	want := "MyMetric 0"
	if got != want {
		t.Errorf("asPrometheus() = %q; want %q", got, want)
	}
}

func TestMetricAsPrometheus_oneDimension(t *testing.T) {
	m := metric{
		Name:          "PoolUsedSpacePercent",
		Dimensions:    map[string]string{"pool": "tank"},
		Value:         75,
		EvalTimestamp: time.Now(),
	}
	got := m.asPrometheus()
	// Should contain metric name, label, and value.
	if !strings.HasPrefix(got, "PoolUsedSpacePercent{") {
		t.Errorf("expected output to start with metric name + '{', got %q", got)
	}
	if !strings.Contains(got, `pool="tank"`) {
		t.Errorf("expected label pool=\"tank\" in output, got %q", got)
	}
	if !strings.HasSuffix(got, "} 75") {
		t.Errorf("expected output to end with '} 75', got %q", got)
	}
}

func TestMetricAsPrometheus_multipleDimensions(t *testing.T) {
	m := metric{
		Name:  "SomeMetric",
		Dimensions: map[string]string{"a": "1", "b": "2"},
		Value: 99,
	}
	got := m.asPrometheus()
	if !strings.HasPrefix(got, "SomeMetric{") {
		t.Errorf("expected 'SomeMetric{...}', got %q", got)
	}
	if !strings.Contains(got, `a="1"`) {
		t.Errorf("expected a=\"1\" in %q", got)
	}
	if !strings.Contains(got, `b="2"`) {
		t.Errorf("expected b=\"2\" in %q", got)
	}
	if !strings.HasSuffix(got, " 99") {
		t.Errorf("expected value 99 at end of %q", got)
	}
}

func TestMetricAsPrometheus_negativeValue(t *testing.T) {
	m := metric{
		Name:  "Delta",
		Value: -5,
	}
	got := m.asPrometheus()
	if !strings.Contains(got, "-5") {
		t.Errorf("expected -5 in output, got %q", got)
	}
}

// --- mon.asPrometheus ---

func TestMonAsPrometheus_singleMetric_hasHelpAndType(t *testing.T) {
	m := &mon{
		cfg: &config.MonitorConfig{},
		metrics: []metric{
			{Name: "MyCounter", Value: 1},
		},
	}
	got := m.asPrometheus()
	if !strings.Contains(got, "# HELP MyCounter") {
		t.Errorf("expected HELP header in output:\n%s", got)
	}
	if !strings.Contains(got, "# TYPE MyCounter untyped") {
		t.Errorf("expected TYPE header in output:\n%s", got)
	}
	if !strings.Contains(got, "MyCounter 1") {
		t.Errorf("expected metric value line in output:\n%s", got)
	}
}

func TestMonAsPrometheus_sameMetricTwice_headerPrintedOnce(t *testing.T) {
	m := &mon{
		cfg: &config.MonitorConfig{},
		metrics: []metric{
			{Name: "LastSnapAge", Dimensions: map[string]string{"fs": "tank/a"}, Value: 100},
			{Name: "LastSnapAge", Dimensions: map[string]string{"fs": "tank/b"}, Value: 200},
		},
	}
	got := m.asPrometheus()
	count := strings.Count(got, "# HELP LastSnapAge")
	if count != 1 {
		t.Errorf("expected HELP header exactly once, got %d times in:\n%s", count, got)
	}
	if !strings.Contains(got, `fs="tank/a"`) {
		t.Errorf("missing tank/a dimension in:\n%s", got)
	}
	if !strings.Contains(got, `fs="tank/b"`) {
		t.Errorf("missing tank/b dimension in:\n%s", got)
	}
}

func TestMonAsPrometheus_differentMetrics_eachHasOwnHeader(t *testing.T) {
	m := &mon{
		cfg: &config.MonitorConfig{},
		metrics: []metric{
			{Name: "MetricA", Value: 1},
			{Name: "MetricB", Value: 2},
		},
	}
	got := m.asPrometheus()
	if !strings.Contains(got, "# HELP MetricA") {
		t.Errorf("missing HELP for MetricA in:\n%s", got)
	}
	if !strings.Contains(got, "# HELP MetricB") {
		t.Errorf("missing HELP for MetricB in:\n%s", got)
	}
	if !strings.Contains(got, "# TYPE MetricA untyped") {
		t.Errorf("missing TYPE for MetricA in:\n%s", got)
	}
	if !strings.Contains(got, "# TYPE MetricB untyped") {
		t.Errorf("missing TYPE for MetricB in:\n%s", got)
	}
}

func TestMonAsPrometheus_noMetrics_emptyOutput(t *testing.T) {
	m := &mon{
		cfg:     &config.MonitorConfig{},
		metrics: nil,
	}
	got := m.asPrometheus()
	if got != "" {
		t.Errorf("expected empty output for no metrics, got %q", got)
	}
}

func TestMonAsPrometheus_ordering_headersBeforeValues(t *testing.T) {
	m := &mon{
		cfg: &config.MonitorConfig{},
		metrics: []metric{
			{Name: "Foo", Value: 7},
		},
	}
	got := m.asPrometheus()
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) < 3 {
		t.Fatalf("expected at least 3 lines (HELP, TYPE, value), got %d:\n%s", len(lines), got)
	}
	if !strings.HasPrefix(lines[0], "# HELP") {
		t.Errorf("line 0 should be HELP, got %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "# TYPE") {
		t.Errorf("line 1 should be TYPE, got %q", lines[1])
	}
	if !strings.HasPrefix(lines[2], "Foo") {
		t.Errorf("line 2 should be metric value, got %q", lines[2])
	}
}
