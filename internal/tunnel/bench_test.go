package tunnel

// A benchmark for the tunnel transport. It runs real QUIC connections through
// a UDP proxy that adds one-way delay and random loss, then reports handshake
// latency, echo round-trip percentiles, and parallel throughput.
//
//	go test ./internal/tunnel -run TestBench -v -args -bench-delay=20ms -bench-loss=1%
//
// It skips unless BOX_BENCH is set, so go test ./... stays quick. With
// -bench-compare the transport is measured twice: once with quic-go's defaults
// and plain stream reads, and once with quicConfig and streamConn, which is
// what a tunnel uses now.

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
)

var (
	benchDelay   = flag.Duration("bench-delay", 20*time.Millisecond, "one-way delay added in each direction")
	benchLoss    = flag.Float64("bench-loss", 0, "packet loss in each direction, 0 to 1")
	benchStreams = flag.Int("bench-streams", 4, "parallel streams in the throughput run")
	benchBytes   = flag.Int64("bench-bytes", 4<<20, "bytes per stream in the throughput run")
	benchEchoes  = flag.Int("bench-echoes", 20, "echo round trips in the latency run")
	benchCompare = flag.Bool("bench-compare", false, "also measure the pre-change transport")
)

// benchProfile is one transport configuration under test.
type benchProfile struct {
	name string
	cfg  *quic.Config
	// batched reads through streamConn instead of raw stream reads.
	batched bool
}

func currentProfile() benchProfile {
	return benchProfile{name: "tunnel", cfg: quicConfig(), batched: true}
}

// legacyProfile is quic-go's defaults before the receive windows and packet
// size were set: 512 KB streams, 1280-byte packets, one small read per call.
func legacyProfile() benchProfile {
	return benchProfile{name: "default", cfg: &quic.Config{
		MaxIdleTimeout:  2 * time.Minute,
		KeepAlivePeriod: 10 * time.Second,
	}}
}

func TestBench(t *testing.T) {
	if os.Getenv("BOX_BENCH") == "" {
		t.Skip("set BOX_BENCH=1 to run the tunnel benchmark")
	}
	profiles := []benchProfile{currentProfile()}
	if *benchCompare {
		profiles = append([]benchProfile{legacyProfile()}, profiles...)
	}
	for _, p := range profiles {
		runBench(t, p)
	}
}

func runBench(t *testing.T, p benchProfile) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	cert, _, err := NewCertificate()
	if err != nil {
		t.Fatal(err)
	}
	ln, err := listenBench(ctx, cert, p)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	proxy, err := newBenchProxy(ctx, ln.Addr().String(), *benchDelay, *benchLoss)
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	sess, err := quic.DialAddr(ctx, proxy.Addr(), &tls.Config{
		InsecureSkipVerify: true, NextProtos: []string{benchALPN},
	}, p.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.CloseWithError(0, "")
	st, err := sess.OpenStreamSync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	handshake := time.Since(start)

	// The latency run also warms the path up, so the throughput run starts
	// from a known congestion window.
	echo := newBenchStream(st, p)
	if err := echo.echoOnce(); err != nil {
		t.Fatal(err)
	}
	rtts := make([]time.Duration, *benchEchoes)
	for i := range rtts {
		begin := time.Now()
		if err := echo.echoOnce(); err != nil {
			t.Fatal(err)
		}
		rtts[i] = time.Since(begin)
	}

	elapsed, bytes, err := benchThroughput(ctx, sess, p)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s: handshake %s, rtt p50 %s p95 %s",
		p.name, handshake.Round(time.Microsecond), percentile(rtts, 0.5), percentile(rtts, 0.95))
	t.Logf("%s: %d streams of %s in %s: %.1f MB/s total, %.1f MB/s per stream",
		p.name, *benchStreams, byteCount(*benchBytes), elapsed.Round(time.Millisecond),
		float64(bytes)/elapsed.Seconds()/1e6, float64(bytes)/elapsed.Seconds()/1e6/float64(*benchStreams))

	stats := sess.ConnectionStats()
	t.Logf("%s: rtt min %s smoothed %s, lost %d of %d packets (%.2f%%)",
		p.name, stats.MinRTT.Round(time.Microsecond), stats.SmoothedRTT.Round(time.Microsecond),
		stats.PacketsLost, stats.PacketsSent, percent(stats.PacketsLost, stats.PacketsSent))
	t.Logf("%s: proxy dropped %d on purpose, %d because its queue was full",
		p.name, proxy.lost.Load(), proxy.overflow.Load())
}

// benchStream carries one request and, for a download, its bytes back.
type benchStream struct {
	st      *quic.Stream
	profile benchProfile
	r       io.Reader
}

func newBenchStream(st *quic.Stream, p benchProfile) *benchStream {
	b := &benchStream{st: st, profile: p, r: st}
	if p.batched {
		b.r = newStreamConn(st, nil, nil, nil)
	}
	return b
}

func (b *benchStream) header(kind byte, size int64) error {
	var h [9]byte
	h[0] = kind
	binary.BigEndian.PutUint64(h[1:], uint64(size))
	_, err := b.st.Write(h[:])
	return err
}

func (b *benchStream) echoOnce() error {
	if _, err := b.st.Write([]byte{'e'}); err != nil {
		return err
	}
	var one [1]byte
	_, err := io.ReadFull(b.r, one[:])
	return err
}

func benchThroughput(ctx context.Context, sess *quic.Conn, p benchProfile) (time.Duration, int64, error) {
	start := time.Now()
	var wg sync.WaitGroup
	var bytes atomic.Int64
	errs := make(chan error, *benchStreams)
	for i := 0; i < *benchStreams; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st, err := sess.OpenStreamSync(ctx)
			if err != nil {
				errs <- err
				return
			}
			b := newBenchStream(st, p)
			if err := b.header('d', *benchBytes); err != nil {
				errs <- err
				return
			}
			n, err := io.CopyBuffer(io.Discard, b.r, make([]byte, 32<<10))
			bytes.Add(n)
			if err != nil {
				errs <- err
			}
			_ = st.Close()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		return 0, 0, err
	}
	return time.Since(start), bytes.Load(), nil
}

func listenBench(ctx context.Context, cert tls.Certificate, p benchProfile) (*quic.Listener, error) {
	ln, err := quic.ListenAddr("127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{cert}, NextProtos: []string{benchALPN},
	}, p.cfg)
	if err != nil {
		return nil, err
	}
	go func() {
		for {
			conn, err := ln.Accept(ctx)
			if err != nil {
				return
			}
			go func() {
				for {
					st, err := conn.AcceptStream(ctx)
					if err != nil {
						return
					}
					go serveBench(st)
				}
			}()
		}
	}()
	return ln, nil
}

const benchALPN = "box-bench"

// serveBench answers one stream: 'e' echoes a byte, 'd' writes the length
// that follows the kind byte.
func serveBench(st *quic.Stream) {
	defer st.Close()
	var kind [1]byte
	if _, err := io.ReadFull(st, kind[:]); err != nil {
		return
	}
	switch kind[0] {
	case 'd':
		var size [8]byte
		if _, err := io.ReadFull(st, size[:]); err != nil {
			return
		}
		left := int64(binary.BigEndian.Uint64(size[:]))
		buf := make([]byte, 32<<10)
		for left > 0 {
			n := min(int64(len(buf)), left)
			if _, err := st.Write(buf[:n]); err != nil {
				return
			}
			left -= n
		}
	case 'e':
		// Echo the kind byte, then every byte after it, so each client
		// write is answered by exactly one write back.
		if _, err := st.Write(kind[:]); err != nil {
			return
		}
		var one [1]byte
		for {
			if _, err := io.ReadFull(st, one[:]); err != nil {
				return
			}
			if _, err := st.Write(one[:]); err != nil {
				return
			}
		}
	}
}

// benchProxy relays UDP between the tunnel peers with a fixed one-way delay
// and random loss, one socket pair per client. Each direction runs as its own
// goroutine over a FIFO delay line with a single timer, so per-packet timers
// do not become the bottleneck.
type benchProxy struct {
	conn     net.PacketConn
	lost     atomic.Int64 // dropped to simulate loss
	overflow atomic.Int64 // dropped because the delay line was full
}

type benchPacket struct {
	data []byte
	at   time.Time
}

type benchLink struct {
	out      net.PacketConn
	addr     net.Addr
	delay    time.Duration
	loss     float64
	ch       chan benchPacket
	lost     *atomic.Int64
	overflow *atomic.Int64
}

func newBenchProxy(ctx context.Context, target string, delay time.Duration, loss float64) (*benchProxy, error) {
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	p := &benchProxy{conn: conn}
	go func() {
		<-ctx.Done()
		conn.Close()
	}()
	go p.serve(ctx, target, delay, loss)
	return p, nil
}

func (p *benchProxy) Addr() string { return p.conn.LocalAddr().String() }

func (p *benchProxy) serve(ctx context.Context, target string, delay time.Duration, loss float64) {
	targetAddr, err := net.ResolveUDPAddr("udp", target)
	if err != nil {
		return
	}
	type peer struct {
		toTarget *benchLink
		toClient *benchLink
	}
	var mu sync.Mutex
	peers := map[string]*peer{}
	buf := make([]byte, 64<<10)
	for {
		n, from, err := p.conn.ReadFrom(buf)
		if err != nil {
			return
		}
		data := append([]byte(nil), buf[:n]...)
		key := from.String()
		mu.Lock()
		pr := peers[key]
		mu.Unlock()
		if pr == nil {
			up, err := net.ListenPacket("udp", "127.0.0.1:0")
			if err != nil {
				continue
			}
			client, err := net.ResolveUDPAddr("udp", key)
			if err != nil {
				up.Close()
				continue
			}
			pr = &peer{
				toTarget: &benchLink{out: up, addr: targetAddr, delay: delay, loss: loss,
					ch: make(chan benchPacket, 8192), lost: &p.lost, overflow: &p.overflow},
				toClient: &benchLink{out: p.conn, addr: client, delay: delay, loss: loss,
					ch: make(chan benchPacket, 8192), lost: &p.lost, overflow: &p.overflow},
			}
			go pr.toTarget.run(ctx)
			go pr.toClient.run(ctx)
			go func(up net.PacketConn, toClient *benchLink) {
				b := make([]byte, 64<<10)
				for {
					n, _, err := up.ReadFrom(b)
					if err != nil {
						return
					}
					toClient.send(append([]byte(nil), b[:n]...))
				}
			}(up, pr.toClient)
			mu.Lock()
			peers[key] = pr
			mu.Unlock()
		}
		pr.toTarget.send(data)
	}
}

// send queues one datagram for the far side. Deadlines stay in order because
// every packet waits the same delay, so the queue is a plain FIFO.
func (l *benchLink) send(data []byte) {
	if l.loss > 0 && rand.Float64() < l.loss {
		l.lost.Add(1)
		return
	}
	select {
	case l.ch <- benchPacket{data: data, at: time.Now().Add(l.delay)}:
	default:
		l.overflow.Add(1)
	}
}

func (l *benchLink) run(ctx context.Context) {
	var queue []benchPacket
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()
	for {
		if len(queue) == 0 {
			select {
			case <-ctx.Done():
				return
			case p := <-l.ch:
				queue = append(queue, p)
			}
			continue
		}
		wait := time.Until(queue[0].at)
		if wait <= 0 {
			_, _ = l.out.WriteTo(queue[0].data, l.addr)
			queue = queue[1:]
			continue
		}
		timer.Reset(wait)
		select {
		case <-ctx.Done():
			return
		case p := <-l.ch:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			queue = append(queue, p)
		case <-timer.C:
		}
	}
}

func percentile(samples []time.Duration, q float64) time.Duration {
	if len(samples) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	i := int(float64(len(sorted)) * q)
	return sorted[min(i, len(sorted)-1)]
}

func percent(part, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return float64(part) / float64(total) * 100
}

func byteCount(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	default:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
}
