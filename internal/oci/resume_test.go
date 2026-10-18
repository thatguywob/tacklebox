package oci

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// stallingBody delivers `good` bytes and then goes silent forever, which is
// what a wedged layer download does. Read blocks rather than returning an
// error, because that is the case no timeout could previously break.
type stallingBody struct {
	data   []byte
	good   int
	off    int
	closed chan struct{}
	once   sync.Once
}

func newStallingBody(data []byte, good int) *stallingBody {
	return &stallingBody{data: data, good: good, closed: make(chan struct{})}
}

func (s *stallingBody) Read(p []byte) (int, error) {
	if s.off < s.good {
		n := copy(p, s.data[s.off:s.good])
		s.off += n
		return n, nil
	}
	// Silent forever, until Close releases us — exactly like a ReadableStream
	// whose read promise never settles.
	<-s.closed
	return 0, io.ErrClosedPipe
}

func (s *stallingBody) Close() error {
	s.once.Do(func() { close(s.closed) })
	return nil
}

func TestResumingReaderResumesAfterStall(t *testing.T) {
	data := bytes.Repeat([]byte("tunaos!"), 4096) // 28 KB
	stallAt := 5000

	var opens []int64
	open := func(offset int64) (io.ReadCloser, error) {
		opens = append(opens, offset)
		if offset == 0 {
			return newStallingBody(data, stallAt), nil
		}
		return io.NopCloser(bytes.NewReader(data[offset:])), nil
	}

	r, err := newResumingReader(open, 50*time.Millisecond, 3)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("content mismatch: got %d bytes, want %d", len(got), len(data))
	}
	// The whole point: it reconnected at the byte it had reached, so the
	// stream is neither truncated nor duplicated.
	if len(opens) != 2 || opens[0] != 0 || opens[1] != int64(stallAt) {
		t.Fatalf("expected reopen at %d, got opens=%v", stallAt, opens)
	}
}

// Without a resume the caller must at least be told, rather than hanging.
func TestResumingReaderGivesUpWithErrStalled(t *testing.T) {
	data := bytes.Repeat([]byte("x"), 1024)
	open := func(offset int64) (io.ReadCloser, error) {
		return newStallingBody(data, 10), nil // always stalls immediately
	}
	r, err := newResumingReader(open, 20*time.Millisecond, 2)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.ReadAll(r)
	if !errors.Is(err, ErrStalled) {
		t.Fatalf("want ErrStalled, got %v", err)
	}
}

func TestResumingReaderPassesThroughCleanStream(t *testing.T) {
	data := bytes.Repeat([]byte("abcd"), 10000)
	n := 0
	open := func(offset int64) (io.ReadCloser, error) {
		n++
		return io.NopCloser(bytes.NewReader(data[offset:])), nil
	}
	r, err := newResumingReader(open, time.Second, 3)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("content mismatch")
	}
	if n != 1 {
		t.Fatalf("healthy stream reconnected %d times, want 1 open", n)
	}
}

// A resumed blob must still hash to its digest: the resume is only correct
// if every byte arrives exactly once, in order.
func TestBlobDigestSurvivesResume(t *testing.T) {
	payload := bytes.Repeat([]byte("layer-content-"), 5000) // 70 KB
	sum := sha256.Sum256(payload)
	digest := "sha256:" + hex.EncodeToString(sum[:])

	var mu sync.Mutex
	served := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.HasSuffix(req.URL.Path, "/token") || strings.Contains(req.URL.Path, "token") {
			fmt.Fprint(w, `{"token":"t"}`)
			return
		}
		rng := req.Header.Get("Range")
		mu.Lock()
		first := served == 0
		served++
		mu.Unlock()

		if first && rng == "" {
			// Truncate mid-body and hang up without a Content-Length promise
			// being met — the client should resume from where it got to.
			w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
			w.WriteHeader(http.StatusOK)
			w.Write(payload[:20000])
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			// Ending the handler closes the body mid-stream.
			return
		}
		var off int
		fmt.Sscanf(rng, "bytes=%d-", &off)
		if off <= 0 || off > len(payload) {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", off, len(payload)-1, len(payload)))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(payload[off:])
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	c.StallTimeout = 200 * time.Millisecond
	rc, err := c.Blob(Ref{Repo: "o/i", Tag: "t"}, Descriptor{Digest: digest, Size: int64(len(payload))})
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// Close is where verifyingReader asserts the digest.
	if err := rc.Close(); err != nil {
		t.Fatalf("digest check failed after resume: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("resumed content differs: got %d bytes want %d", len(got), len(payload))
	}
}

// A registry that ignores Range would otherwise replay the prefix into the
// middle of the stream, corrupting the layer and failing the digest with a
// confusing message. Reject it explicitly instead.
func TestResumeRejectsRangeIgnoringRegistry(t *testing.T) {
	payload := bytes.Repeat([]byte("z"), 1000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.Contains(req.URL.Path, "token") {
			fmt.Fprint(w, `{"token":"t"}`)
			return
		}
		w.WriteHeader(http.StatusOK) // 200 even for a Range request
		w.Write(payload)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	_, err := c.getRange("o/i", "blobs/sha256:deadbeef", "", 500)
	if err == nil || !strings.Contains(err.Error(), "resume from 500 unsupported") {
		t.Fatalf("want explicit resume-unsupported error, got %v", err)
	}
}

// Recorded-stall reproduction: mirrors local://iso-builder-baseline.json,
// where a layer went quiet at byte 12582912 (12 MiB). The reader must either
// recover via a byte-exact Range resume (opens == [0, 12582912]) or fail with
// ErrStalled carrying the stalled offset — never hang, never duplicate bytes.
func TestResumingReaderRecoversAt12582912(t *testing.T) {
	const stallAt = 12582912
	const tail = 4096
	data := make([]byte, stallAt+tail)
	for i := range data {
		data[i] = byte((i * 31) % 251)
	}

	var mu sync.Mutex
	var opens []int64
	open := func(offset int64) (io.ReadCloser, error) {
		mu.Lock()
		opens = append(opens, offset)
		mu.Unlock()
		if offset == 0 {
			return newStallingBody(data, stallAt), nil
		}
		return io.NopCloser(bytes.NewReader(data[offset:])), nil
	}

	r, err := newResumingReader(open, 50*time.Millisecond, 3)
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		got []byte
		err error
	}
	done := make(chan result, 1)
	go func() {
		got, err := io.ReadAll(r)
		done <- result{got, err}
	}()
	select {
	case res := <-done:
		if res.err != nil {
			if !errors.Is(res.err, ErrStalled) {
				t.Fatalf("read: %v", res.err)
			}
			if !strings.Contains(res.err.Error(), "12582912") {
				t.Fatalf("ErrStalled must carry stalled offset, got: %v", res.err)
			}
			return
		}
		if !bytes.Equal(res.got, data) {
			t.Fatalf("content mismatch: got %d bytes, want %d", len(res.got), len(data))
		}
		mu.Lock()
		defer mu.Unlock()
		if len(opens) != 2 || opens[0] != 0 || opens[1] != stallAt {
			t.Fatalf("expected byte-exact resume opens=[0 %d], got %v", stallAt, opens)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("reader hung: neither recovered nor returned ErrStalled")
	}
}

// Data+error chunk: a body delivering final bytes together with a transport
// error. The stashed bytes must drain first and the resume must start after
// them, so no byte arrives twice and the stream still verifies.
func TestResumingReaderDataPlusErrorChunk(t *testing.T) {
	payload := bytes.Repeat([]byte("layer-bytes-0123456789"), 4000) // ~84 KB
	sum := sha256.Sum256(payload)
	want := "sha256:" + hex.EncodeToString(sum[:])
	const firstGood = 50000
	boom := errors.New("connection reset by peer")

	type flakyBody struct {
		data []byte
		off  int
		sent bool
	}
	open := func(offset int64) (io.ReadCloser, error) {
		if offset == 0 {
			b := &flakyBody{data: payload}
			return readCloserFunc{
				read: func(p []byte) (int, error) {
					if b.off < firstGood {
						n := copy(p, b.data[b.off:firstGood])
						b.off += n
						if b.off >= firstGood && !b.sent {
							b.sent = true
							return n, boom
						}
						return n, nil
					}
					return 0, boom
				},
				close: func() error { return nil },
			}, nil
		}
		return io.NopCloser(bytes.NewReader(payload[offset:])), nil
	}

	r, err := newResumingReader(open, 50*time.Millisecond, 3)
	if err != nil {
		t.Fatal(err)
	}
	vr := &verifyingReader{body: r, want: want, hash: sha256.New()}
	got, err := io.ReadAll(vr)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("duplication/corruption after data+error resume: got %d bytes want %d", len(got), len(payload))
	}
	if err := vr.Close(); err != nil {
		t.Fatalf("digest check failed after data+error resume: %v", err)
	}
}

type readCloserFunc struct {
	read  func([]byte) (int, error)
	close func() error
}

func (f readCloserFunc) Read(p []byte) (int, error) { return f.read(p) }
func (f readCloserFunc) Close() error {
	if f.close != nil {
		return f.close()
	}
	return nil
}

// Truncated stream: ends early with the wrong sha256. Close must report a
// digest mismatch, so partial bytes are never mistaken for a complete layer.
func TestVerifyingReaderRejectsTruncatedStream(t *testing.T) {
	full := bytes.Repeat([]byte("complete-layer-"), 5000)
	sum := sha256.Sum256(full)
	want := "sha256:" + hex.EncodeToString(sum[:])
	truncated := full[:len(full)/3]

	vr := &verifyingReader{body: io.NopCloser(bytes.NewReader(truncated)), want: want, hash: sha256.New()}
	got, err := io.ReadAll(vr)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, truncated) {
		t.Fatalf("expected partial %d bytes, got %d", len(truncated), len(got))
	}
	err = vr.Close()
	if err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("want digest mismatch on truncated stream, got %v", err)
	}
}

// Zero-byte trailing chunk: a (0, nil) read followed by EOF must not corrupt
// the stream or wedge the pump — the digest still verifies.
func TestResumingReaderToleratesZeroByteChunk(t *testing.T) {
	payload := bytes.Repeat([]byte("zero-chunk-"), 3000)
	sum := sha256.Sum256(payload)
	want := "sha256:" + hex.EncodeToString(sum[:])

	calls := 0
	off := 0
	open := func(offset int64) (io.ReadCloser, error) {
		if offset != 0 {
			return io.NopCloser(bytes.NewReader(payload[offset:])), nil
		}
		return readCloserFunc{
			read: func(p []byte) (int, error) {
				calls++
				if calls == 1 {
					return 0, nil // zero-byte trailing-style chunk
				}
				if off >= len(payload) {
					return 0, io.EOF
				}
				n := copy(p, payload[off:])
				off += n
				return n, nil
			},
			close: func() error { return nil },
		}, nil
	}
	r, err := newResumingReader(open, time.Second, 3)
	if err != nil {
		t.Fatal(err)
	}
	vr := &verifyingReader{body: r, want: want, hash: sha256.New()}
	got, err := io.ReadAll(vr)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("content mismatch after zero-byte chunk: got %d want %d", len(got), len(payload))
	}
	if err := vr.Close(); err != nil {
		t.Fatalf("digest check failed: %v", err)
	}
}

// Reads after Close must fail fast with errReaderClosed and never trigger a
// fresh Range reopen (no reopen storm on a discarded stream).
func TestResumingReaderReadsAfterCloseFailFast(t *testing.T) {
	data := bytes.Repeat([]byte("abcd"), 10000)
	var mu sync.Mutex
	opens := 0
	open := func(offset int64) (io.ReadCloser, error) {
		mu.Lock()
		opens++
		mu.Unlock()
		return io.NopCloser(bytes.NewReader(data[offset:])), nil
	}
	r, err := newResumingReader(open, time.Second, 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	for i := range 5 {
		n, err := r.Read(make([]byte, 1024))
		if n != 0 || !errors.Is(err, errReaderClosed) {
			t.Fatalf("read %d after Close: got n=%d err=%v, want errReaderClosed", i, n, err)
		}
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("reads after Close too slow: %v", elapsed)
	}
	mu.Lock()
	defer mu.Unlock()
	if opens != 1 {
		t.Fatalf("Reads after Close reopened the stream: opens=%d, want 1", opens)
	}
}
