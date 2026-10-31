// Package metrics exposes runtime and application metrics in the
// Prometheus text format without external dependencies.
package metrics

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"runtime/metrics"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Counter struct{ v atomic.Int64 }

func (c *Counter) Inc()         { c.v.Add(1) }
func (c *Counter) Add(n int64)  { c.v.Add(n) }
func (c *Counter) Value() int64 { return c.v.Load() }

type Gauge struct{ v atomic.Int64 }

func (g *Gauge) Inc()         { g.v.Add(1) }
func (g *Gauge) Dec()         { g.v.Add(-1) }
func (g *Gauge) Set(n int64)  { g.v.Store(n) }
func (g *Gauge) Value() int64 { return g.v.Load() }

// Histogram with fixed buckets (seconds).
type Histogram struct {
	mu      sync.Mutex
	buckets []float64
	counts  []uint64
	sum     float64
	count   uint64
}

func NewHistogram(buckets ...float64) *Histogram {
	return &Histogram{buckets: buckets, counts: make([]uint64, len(buckets))}
}

func (h *Histogram) Observe(v float64) {
	h.mu.Lock()
	for i, b := range h.buckets {
		if v <= b {
			h.counts[i]++
		}
	}
	h.sum += v
	h.count++
	h.mu.Unlock()
}

func (h *Histogram) Since(t time.Time) { h.Observe(time.Since(t).Seconds()) }

// Labeled counter keyed by a small, bounded label set.
type CounterVec struct {
	mu sync.Mutex
	m  map[string]*Counter
}

func (v *CounterVec) With(label string) *Counter {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.m == nil {
		v.m = map[string]*Counter{}
	}
	c := v.m[label]
	if c == nil {
		c = &Counter{}
		v.m[label] = c
	}
	return c
}

// Registry of application metrics.
type Registry struct {
	start time.Time

	HTTPRequests CounterVec // label: status class
	HTTPDuration *Histogram
	SyncRuns     CounterVec // label: result
	SyncDuration *Histogram
	MessagesSync Counter
	IMAPErrors   Counter
	SMTPErrors   Counter
	SMTPSent     Counter
	IMAPConns    Gauge
	IdleConns    Gauge
	OAuthRefresh Counter
	Extra        func(w io.Writer) // optional dynamic gauges
}

func New() *Registry {
	return &Registry{
		start:        time.Now(),
		HTTPDuration: NewHistogram(0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10),
		SyncDuration: NewHistogram(0.1, 0.5, 1, 2, 5, 10, 30, 60, 120, 300, 600),
	}
}

var runtimeSamples = []metrics.Sample{
	{Name: "/cpu/classes/total:cpu-seconds"},
	{Name: "/cpu/classes/idle:cpu-seconds"},
	{Name: "/memory/classes/total:bytes"},
	{Name: "/memory/classes/heap/objects:bytes"},
	{Name: "/gc/cycles/total:gc-cycles"},
}

// Write renders all metrics.
func (r *Registry) Write(w io.Writer) {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	samples := make([]metrics.Sample, len(runtimeSamples))
	copy(samples, runtimeSamples)
	metrics.Read(samples)
	val := func(i int) float64 {
		switch samples[i].Value.Kind() {
		case metrics.KindFloat64:
			return samples[i].Value.Float64()
		case metrics.KindUint64:
			return float64(samples[i].Value.Uint64())
		}
		return 0
	}
	g := func(name, help string, v any) {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s gauge\n%s %v\n", name, help, name, name, v)
	}
	c := func(name, help string, v any) {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n%s %v\n", name, help, name, name, v)
	}
	g("lightmail_uptime_seconds", "Process uptime.", int64(time.Since(r.start).Seconds()))
	g("lightmail_go_heap_alloc_bytes", "Go heap bytes in use.", ms.HeapAlloc)
	g("lightmail_go_sys_bytes", "Bytes obtained from the OS by the Go runtime.", ms.Sys)
	g("lightmail_go_memory_total_bytes", "All memory mapped by the Go runtime.", val(2))
	g("lightmail_goroutines", "Number of goroutines.", runtime.NumGoroutine())
	c("lightmail_cpu_seconds_total", "CPU time consumed by the process (Go runtime estimate: total minus idle).", fmt.Sprintf("%.3f", max(0, val(0)-val(1))))
	if rss := processRSS(); rss > 0 {
		g("lightmail_process_resident_bytes", "Resident memory of the process (VmRSS).", rss)
	}
	c("lightmail_gc_cycles_total", "Completed GC cycles.", val(4))
	writeVec(w, "lightmail_http_requests_total", "HTTP requests by status class.", "class", &r.HTTPRequests)
	writeHist(w, "lightmail_http_request_duration_seconds", "HTTP request latency.", r.HTTPDuration)
	writeVec(w, "lightmail_sync_runs_total", "Account synchronisations by result.", "result", &r.SyncRuns)
	writeHist(w, "lightmail_sync_duration_seconds", "Duration of account synchronisations.", r.SyncDuration)
	c("lightmail_messages_synced_total", "Message headers stored by the sync engine.", r.MessagesSync.Value())
	c("lightmail_imap_errors_total", "IMAP errors.", r.IMAPErrors.Value())
	c("lightmail_smtp_errors_total", "SMTP submission errors.", r.SMTPErrors.Value())
	c("lightmail_smtp_sent_total", "Messages submitted successfully.", r.SMTPSent.Value())
	g("lightmail_imap_connections", "Open IMAP connections (pool + IDLE).", r.IMAPConns.Value())
	g("lightmail_imap_idle_connections", "IMAP connections in IDLE.", r.IdleConns.Value())
	c("lightmail_oauth_refresh_total", "OAuth2 token refreshes.", r.OAuthRefresh.Value())
	if r.Extra != nil {
		r.Extra(w)
	}
}

func writeVec(w io.Writer, name, help, label string, v *CounterVec) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n", name, help, name)
	v.mu.Lock()
	keys := make([]string, 0, len(v.m))
	for k := range v.m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(w, "%s{%s=%q} %d\n", name, label, k, v.m[k].Value())
	}
	v.mu.Unlock()
}

func writeHist(w io.Writer, name, help string, h *Histogram) {
	h.mu.Lock()
	defer h.mu.Unlock()
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s histogram\n", name, help, name)
	for i, b := range h.buckets {
		fmt.Fprintf(w, "%s_bucket{le=\"%s\"} %d\n", name, strings.TrimRight(strings.TrimRight(fmt.Sprintf("%f", b), "0"), "."), h.counts[i])
	}
	fmt.Fprintf(w, "%s_bucket{le=\"+Inf\"} %d\n%s_sum %f\n%s_count %d\n", name, h.count, name, h.sum, name, h.count)
}

// Snapshot returns a few values for the status endpoint.
func (r *Registry) Snapshot() map[string]any {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	s := []metrics.Sample{{Name: "/cpu/classes/total:cpu-seconds"}, {Name: "/cpu/classes/idle:cpu-seconds"}}
	metrics.Read(s)
	cpu := 0.0
	if s[0].Value.Kind() == metrics.KindFloat64 && s[1].Value.Kind() == metrics.KindFloat64 {
		cpu = max(0, s[0].Value.Float64()-s[1].Value.Float64())
	}
	return map[string]any{
		"uptimeSeconds":   int64(time.Since(r.start).Seconds()),
		"heapAllocBytes":  ms.HeapAlloc,
		"sysBytes":        ms.Sys,
		"rssBytes":        processRSS(),
		"goroutines":      runtime.NumGoroutine(),
		"cpuSeconds":      cpu,
		"imapConnections": r.IMAPConns.Value(),
		"idleConnections": r.IdleConns.Value(),
		"messagesSynced":  r.MessagesSync.Value(),
		"imapErrors":      r.IMAPErrors.Value(),
		"smtpErrors":      r.SMTPErrors.Value(),
		"smtpSent":        r.SMTPSent.Value(),
	}
}

// processRSS returns the resident set size on Linux (0 elsewhere).
func processRSS() int64 {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			var kb int64
			fmt.Sscanf(strings.TrimSpace(strings.TrimPrefix(line, "VmRSS:")), "%d", &kb)
			return kb * 1024
		}
	}
	return 0
}
