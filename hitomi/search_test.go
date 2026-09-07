package hitomi

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testWords(values ...uint32) []byte {
	var data []byte
	for _, value := range values {
		data = binary.BigEndian.AppendUint32(data, value)
	}
	return data
}

// A single-key node encoded independently from the production decoder.
func testNode(hash string, offset uint64, length uint32, left, right uint64) []byte {
	key, err := hex.DecodeString(hash)
	if err != nil {
		panic(err)
	}
	data := testWords(1, 4)
	data = append(data, key...)
	data = binary.BigEndian.AppendUint32(data, 1)
	data = binary.BigEndian.AppendUint64(data, offset)
	data = binary.BigEndian.AppendUint32(data, length)
	data = binary.BigEndian.AppendUint64(data, left)
	data = binary.BigEndian.AppendUint64(data, right)
	for range 15 {
		data = binary.BigEndian.AppendUint64(data, 0)
	}
	return data
}

func testIndexFiles() map[string][]byte {
	// Known SHA-256 prefixes: alpha=8ed3f6ad, gamma=be9d587d, beta=f44e64e7.
	// Nodes cover both child branches; the final node has no 4 KiB padding.
	index := make([]byte, 8192)
	copy(index, testNode("be9d587d", 40, 12, 4096, 8192))
	copy(index[4096:], testNode("8ed3f6ad", 8, 16, 0, 0))
	index = append(index, testNode("f44e64e7", 24, 16, 0, 0)...)
	data := make([]byte, 8)
	data = append(data, testWords(3, 90, 50, 10)...)
	data = append(data, testWords(3, 80, 50, 30)...)
	data = append(data, testWords(2, 50, 40)...)
	return map[string][]byte{
		"/galleriesindex/version":             []byte("123\n"),
		"/galleriesindex/galleries.123.index": index,
		"/galleriesindex/galleries.123.data":  data,
		"/index-chinese.nozomi":               testWords(80, 50, 10),
	}
}

func testIndexServer(t *testing.T, files map[string][]byte, ignoreRange bool) (*HitomiClient, *atomic.Int32) {
	t.Helper()
	var versions atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Referer") != "https://hitomi.la/" {
			t.Errorf("unexpected request: %s %s, Referer=%q", r.Method, r.URL, r.Header.Get("Referer"))
		}
		if r.URL.Path == "/galleriesindex/version" {
			versions.Add(1)
			if r.URL.Query().Get("_") == "" {
				t.Error("index version request lacks a cache-busting timestamp")
			}
		} else if r.URL.RawQuery != "" {
			t.Errorf("search value leaked into URL query: %s", r.URL)
		}
		if strings.HasSuffix(r.URL.Path, ".index") || strings.HasSuffix(r.URL.Path, ".data") {
			if r.Header.Get("Range") == "" {
				t.Error("index/data lookup lacks Range")
			}
		}
		data, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if ignoreRange {
			w.Write(data)
			return
		}
		http.ServeContent(w, r, "index", time.Time{}, bytes.NewReader(data))
	}))
	t.Cleanup(server.Close)
	return &HitomiClient{HTTPClient: server.Client(), BaseURL: server.URL}, &versions
}

func TestSearchIDsBooleanQueries(t *testing.T) {
	files := map[string][]byte{
		"/index-chinese.nozomi":          testWords(9, 7, 5, 3, 3),
		"/artist/alice smith-all.nozomi": testWords(9, 7, 5),
		"/series/sample-all.nozomi":      testWords(7, 5, 3),
		"/character/bob-all.nozomi":      testWords(9, 5, 2),
		"/type/manga-all.nozomi":         testWords(5),
		"/group/a/b?c#d-all.nozomi":      testWords(7),
		"/tag/female-example-all.nozomi": testWords(7, 9),
		"/tag/male-example-all.nozomi":   testWords(9),
	}
	client, versions := testIndexServer(t, files, false)
	for _, tc := range []struct {
		query string
		want  []int
	}{
		{" LANGUAGE:CHINESE \t", []int{9, 7, 5, 3}},
		{"language:chinese artist:alice_smith", []int{9, 7, 5}},
		{"language:chinese artist:alice_smith OR series:sample -type:manga", []int{9, 7, 3}},
		{"artist:alice_smith OR series:sample character:bob OR type:manga", []int{9, 5}},
		{"series:sample OR character:bob OR type:manga", []int{9, 7, 5, 3, 2}},
		{"artist:alice_smith artist:alice_smith -artist:alice_smith", []int{}},
		{"language:chinese -character:bob -type:manga", []int{7, 3}},
		{"female:example male:example", []int{9}},
		{"group:a/b?c#d", []int{7}},
		{"artist:missing", []int{}},
		{"artist:missing language:chinese", []int{}},
		{"artist:missing OR type:manga", []int{5}},
		{"language:chinese -artist:missing", []int{9, 7, 5, 3}},
		{"", []int{}},
		{" \n ", []int{}},
		{"-artist:alice_smith", []int{}},
	} {
		t.Run(tc.query, func(t *testing.T) {
			got, err := client.SearchIDs(t.Context(), tc.query)
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("SearchIDs(%q) = %v, %v; want %v", tc.query, got, err, tc.want)
			}
		})
	}
	if versions.Load() != 0 {
		t.Fatal("tag-only searches must not fetch the gallery index version")
	}
}

func TestSearchIDsInvalidQueries(t *testing.T) {
	client := &HitomiClient{BaseURL: ":invalid"}
	for _, query := range []string{"or", "or a", "a or", "a or or b", "-", "a or -b", "-a or b"} {
		t.Run(query, func(t *testing.T) {
			if _, err := client.SearchIDs(t.Context(), query); err == nil || !strings.Contains(err.Error(), "hitomi:") || strings.Contains(err.Error(), "GET") {
				t.Fatalf("expected query error before any HTTP request; got %v", err)
			}
		})
	}
}

func TestSearchIDsBTree(t *testing.T) {
	for _, ignoreRange := range []bool{false, true} {
		t.Run(fmt.Sprintf("ignore_range=%v", ignoreRange), func(t *testing.T) {
			client, versions := testIndexServer(t, testIndexFiles(), ignoreRange)
			for _, tc := range []struct {
				query string
				want  []int
			}{
				{"ALPHA", []int{90, 50, 10}},
				{"beta", []int{80, 50, 30}},
				{"gamma", []int{50, 40}},
				{"alpha OR beta", []int{90, 80, 50, 30, 10}},
				{"alpha OR beta gamma", []int{50}},
				{"alpha OR beta -gamma", []int{90, 80, 30, 10}},
				{"language:chinese alpha OR beta", []int{80, 50, 10}},
				{"alpha OR alpha alpha", []int{90, 50, 10}},
				{"missing", []int{}},
				{"unknown:namespace", []int{}},
			} {
				t.Run(tc.query, func(t *testing.T) {
					before := versions.Load()
					got, err := client.SearchIDs(t.Context(), tc.query)
					if err != nil || !reflect.DeepEqual(got, tc.want) {
						t.Fatalf("SearchIDs(%q) = %v, %v; want %v", tc.query, got, err, tc.want)
					}
					if versions.Load()-before != 1 {
						t.Error("expected exactly one version fetch per text query")
					}
				})
			}
		})
	}
}

func TestSearchIDsMalformedIndexes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		query  string
		modify func(map[string][]byte)
	}{
		{"empty version", "alpha", func(f map[string][]byte) { f["/galleriesindex/version"] = []byte(" \n") }},
		{"missing version", "alpha", func(f map[string][]byte) { delete(f, "/galleriesindex/version") }},
		{"missing index", "alpha", func(f map[string][]byte) { delete(f, "/galleriesindex/galleries.123.index") }},
		{"missing data", "gamma", func(f map[string][]byte) { delete(f, "/galleriesindex/galleries.123.data") }},
		{"truncated node", "alpha", func(f map[string][]byte) { f["/galleriesindex/galleries.123.index"] = testWords(1) }},
		{"invalid count", "gamma", func(f map[string][]byte) { binary.BigEndian.PutUint32(f["/galleriesindex/galleries.123.data"][40:], 3) }},
		{"truncated data", "gamma", func(f map[string][]byte) {
			f["/galleriesindex/galleries.123.data"] = f["/galleriesindex/galleries.123.data"][:49]
		}},
		{"truncated nozomi", "language:chinese", func(f map[string][]byte) { f["/index-chinese.nozomi"] = []byte{1, 2, 3} }},
		{"cyclic node", "alpha", func(f map[string][]byte) {
			copy(f["/galleriesindex/galleries.123.index"][4096:], testNode("be9d587d", 40, 12, 4096, 0))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := testIndexFiles()
			tc.modify(files)
			client, _ := testIndexServer(t, files, false)
			if ids, err := client.SearchIDs(t.Context(), tc.query); err == nil {
				t.Fatalf("corrupt/missing index must return an error, got %v", ids)
			}
		})
	}
}

func TestDecodeSearchNodeRejectsMalformedData(t *testing.T) {
	valid := testNode("be9d587d", 40, 12, 4096, 8192)
	for size := 0; size < len(valid); size++ {
		if _, err := decodeSearchNode(valid[:size]); err == nil {
			t.Fatalf("accepted node truncated to %d bytes", size)
		}
	}
	for _, tc := range []struct {
		offset int
		value  uint32
	}{{0, 0xffffffff}, {4, 0xffffffff}, {12, 2}, {24, 3}, {24, 0xffffffff}} {
		data := bytes.Clone(valid)
		binary.BigEndian.PutUint32(data[tc.offset:], tc.value)
		if _, err := decodeSearchNode(data); err == nil {
			t.Errorf("accepted invalid value %d at offset %d", tc.value, tc.offset)
		}
	}
}

func TestSearchIDsRangeValidation(t *testing.T) {
	for _, header := range []string{"", "bytes 1-4/8", "bytes 0-2/8", "bytes 0-4/8", "bytes 0-3/3", "items 0-3/8"} {
		t.Run(header, func(t *testing.T) {
			if err := validateSearchRange(header, searchRange{0, 4}, 4); err == nil {
				t.Errorf("accepted invalid Content-Range %q", header)
			}
		})
	}
	for _, header := range []string{"bytes 0-3/8", "bytes 0-3/*"} {
		if err := validateSearchRange(header, searchRange{0, 4}, 4); err != nil {
			t.Errorf("rejected valid Content-Range: %v", err)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/galleriesindex/version" {
			w.Write([]byte("123"))
			return
		}
		w.Header().Set("Content-Range", "bytes 1-164/4096")
		w.WriteHeader(http.StatusPartialContent)
		w.Write(testNode("be9d587d", 40, 12, 0, 0))
	}))
	defer server.Close()
	client := &HitomiClient{HTTPClient: server.Client(), BaseURL: server.URL}
	if _, err := client.SearchIDs(t.Context(), "gamma"); err == nil || !strings.Contains(err.Error(), "Content-Range") {
		t.Fatalf("misaligned partial response was not rejected: %v", err)
	}
}

func TestSearchIDsRetriesAndHTTPFailures(t *testing.T) {
	for _, status := range []int{http.StatusServiceUnavailable, http.StatusTooManyRequests, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if requests.Add(1) == 1 || status == http.StatusForbidden {
					w.WriteHeader(status)
					return
				}
				w.Write(testWords(42))
			}))
			defer server.Close()
			client := &HitomiClient{HTTPClient: server.Client(), BaseURL: server.URL}
			ids, err := client.SearchIDs(t.Context(), "language:chinese")
			if status == http.StatusForbidden {
				if err == nil || requests.Load() != 1 {
					t.Fatalf("403 must fail without retry, requests=%d, error=%v", requests.Load(), err)
				}
			} else if err != nil || requests.Load() != 2 || !reflect.DeepEqual(ids, []int{42}) {
				t.Fatalf("transient failure was not retried correctly: ids=%v, requests=%d, error=%v", ids, requests.Load(), err)
			}
		})
	}
}

func TestSearchIDsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := SearchIDs(ctx, "alpha"); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled search = %v", err)
	}
	for _, retry := range []bool{false, true} {
		t.Run(fmt.Sprintf("during_retry=%v", retry), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			started := make(chan struct{}, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				started <- struct{}{}
				if retry {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				<-r.Context().Done()
			}))
			defer server.Close()
			client := &HitomiClient{HTTPClient: server.Client(), BaseURL: server.URL}
			done := make(chan error, 1)
			go func() {
				_, err := client.SearchIDs(ctx, "language:chinese")
				done <- err
			}()
			<-started
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("canceled request returned %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("search did not honor cancellation")
			}
		})
	}
}

func TestSearchIDsConcurrencyAndDeduplication(t *testing.T) {
	var active, maximum, requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		n := active.Add(1)
		defer active.Add(-1)
		for old := maximum.Load(); n > old; old = maximum.Load() {
			if maximum.CompareAndSwap(old, n) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		w.Write(testWords(2, 1, 2))
	}))
	defer server.Close()
	client := &HitomiClient{HTTPClient: server.Client(), BaseURL: server.URL, MaxConcurrency: 2}
	ids, err := client.SearchIDs(t.Context(), "artist:a OR artist:b OR artist:c OR artist:d artist:a")
	if err != nil || !reflect.DeepEqual(ids, []int{2, 1}) {
		t.Fatalf("SearchIDs = %v, %v", ids, err)
	}
	if maximum.Load() != 2 || requests.Load() != 4 {
		t.Fatalf("maximum=%d requests=%d; want maximum=2, requests=4", maximum.Load(), requests.Load())
	}

	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			ids, err := client.SearchIDs(t.Context(), "artist:a")
			if err != nil || !reflect.DeepEqual(ids, []int{2, 1}) {
				t.Errorf("shared client search = %v, %v", ids, err)
			}
		})
	}
	workers.Wait()
}
