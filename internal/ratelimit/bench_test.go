package ratelimit_test

import (
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"gateway/internal/ratelimit"
)

// BenchmarkAllowParallel measures Allow from many goroutines across many
// client keys, the case where one lock serialises unrelated clients.
func BenchmarkAllowParallel(b *testing.B) {
	for _, clients := range []int{1, 1_000, 100_000} {
		b.Run(strconv.Itoa(clients)+"-clients", func(b *testing.B) {
			keys := make([]string, clients)
			for i := range keys {
				keys[i] = "10.0." + strconv.Itoa(i/256) + "." + strconv.Itoa(i%256)
			}
			l := ratelimit.New(1e9, 1e9, nil)
			var next atomic.Uint64
			b.RunParallel(func(pb *testing.PB) {
				i := next.Add(7919)
				for pb.Next() {
					i++
					l.Allow(keys[i%uint64(clients)])
				}
			})
		})
	}
}

// BenchmarkSweepPause measures the slowest Allow call when a sweep runs over
// many idle buckets: every other client waits for it.
func BenchmarkSweepPause(b *testing.B) {
	const clients = 200_000
	keys := make([]string, clients)
	for i := range keys {
		keys[i] = "k" + strconv.Itoa(i)
	}
	for range b.N {
		b.StopTimer()
		now := time.Unix(1_700_000_000, 0)
		l := ratelimit.New(10, 10, func() time.Time { return now })
		for _, k := range keys {
			l.Allow(k)
		}
		now = now.Add(2 * time.Minute) // every bucket is full again and due for a sweep
		var worst time.Duration
		b.StartTimer()
		for _, k := range keys[:2_000] {
			start := time.Now()
			l.Allow(k)
			worst = max(worst, time.Since(start))
		}
		b.ReportMetric(float64(worst.Microseconds()), "worst-µs")
	}
}
