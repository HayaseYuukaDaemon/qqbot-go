// Search logic adapted from HayaseYuukaDaemon/Hitomi_Downloader, hitomiv2.py
// (revision 721730d), under GPL-3.0. See LICENSE in this directory.
// Modified 2026-09-06: ported to Go as a search-only client.

package hitomi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultSearchBaseURL = "https://ltn.gold-usergeneratedcontent.net"
	Referer              = "https://hitomi.la/"
	btreeOrder           = 16
	btreeNodeSize        = 4096
	maxSearchResponse    = 64 << 20
	searchAttempts       = 3
)

var (
	errSearchNotFound = errors.New("search resource not found")
)

// HitomiClient searches Hitomi's gallery indexes without fetching gallery
// metadata or images. Its zero value is ready to use. A client may be shared
// between goroutines, provided its fields are not changed during use.
type HitomiClient struct {
	// HTTPClient defaults to a client with a 20-second request timeout and
	// standard HTTP_PROXY/HTTPS_PROXY support. Supply a client for a custom proxy.
	HTTPClient *http.Client
	// BaseURL defaults to https://ltn.gold-usergeneratedcontent.net.
	BaseURL string
	// MaxConcurrency limits simultaneous term lookups per search (default 5).
	MaxConcurrency int
}

// SearchIDs returns unique gallery IDs, newest (highest ID) first.
// Whitespace means AND, "OR" joins adjacent positive terms, and a leading "-"
// excludes a term. Underscores represent spaces inside a term. Empty queries
// and queries containing only exclusions return an empty slice.
// No language filter is added; include language:chinese when desired.
func SearchIDs(ctx context.Context, query string) ([]int, error) {
	return new(HitomiClient).SearchIDs(ctx, query)
}

type searchQuery struct {
	groups   [][]string // Union within each group, intersection between groups.
	excluded []string
}

func NewHitomiClient(httpClient *http.Client, maxConcurrency int) *HitomiClient {
	c := &HitomiClient{
		HTTPClient:     httpClient,
		BaseURL:        defaultSearchBaseURL,
		MaxConcurrency: maxConcurrency,
	}
	if c.HTTPClient == nil {
		c.HTTPClient = http.DefaultClient
	}
	if c.BaseURL == "" {
		c.BaseURL = defaultSearchBaseURL
	}
	if c.MaxConcurrency <= 0 {
		c.MaxConcurrency = 5
	}
	return c
}

type galleryIDs map[int]struct{}

// SearchIDs runs a search using this client's transport and concurrency settings.
// The gallery index version is fetched once per query, only for text searches.
// Missing tags/keywords return no matches; transport and index errors are returned.
func (c *HitomiClient) SearchIDs(ctx context.Context, query string) ([]int, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	parsed, err := parseSearchQuery(query)
	if err != nil {
		return nil, err
	}
	if len(parsed.groups) == 0 {
		return []int{}, nil
	}

	terms := []string{}
	positions := make(map[string]int)
	needsVersion := false
	addTerm := func(term string) {
		if _, exists := positions[term]; exists {
			return
		}
		positions[term] = len(terms)
		terms = append(terms, term)
		if _, ok := nozomiPath(term); !ok {
			needsVersion = true
		}
	}
	for _, group := range parsed.groups {
		for _, term := range group {
			addTerm(term)
		}
	}
	for _, term := range parsed.excluded {
		addTerm(term)
	}

	var version string
	if needsVersion {
		data, err := c.get(ctx, "galleriesindex/version?_="+strconv.FormatInt(time.Now().UnixMilli(), 10), nil)
		if err != nil {
			return nil, fmt.Errorf("hitomi: get index version: %w", err)
		}
		version = strings.TrimSpace(string(data))
		if version == "" || len(version) > 128 || strings.ContainsAny(version, "/\\\r\n\t ") {
			return nil, errors.New("hitomi: invalid gallery index version")
		}
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make([]galleryIDs, len(terms))
	jobs := make(chan int)
	var workers sync.WaitGroup
	var firstError error
	var errorOnce sync.Once
	for range min(c.MaxConcurrency, len(terms)) {
		workers.Go(func() {
			for i := range jobs {
				ids, err := c.searchTerm(ctx, terms[i], version)
				if err != nil {
					errorOnce.Do(func() {
						firstError = fmt.Errorf("hitomi: search %q: %w", terms[i], err)
						cancel()
					})
					return
				}
				results[i] = ids
			}
		})
	}
enqueue:
	for i := range terms {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break enqueue
		}
	}
	close(jobs)
	workers.Wait()
	if firstError != nil {
		return nil, firstError
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var current galleryIDs
	for _, group := range parsed.groups {
		union := make(galleryIDs)
		for _, term := range group {
			for id := range results[positions[term]] {
				union[id] = struct{}{}
			}
		}
		if current == nil {
			current = union
		} else {
			for id := range current {
				if _, ok := union[id]; !ok {
					delete(current, id)
				}
			}
		}
		if len(current) == 0 {
			return []int{}, nil
		}
	}
	for _, term := range parsed.excluded {
		for id := range results[positions[term]] {
			delete(current, id)
		}
	}
	ids := make([]int, 0, len(current))
	for id := range current {
		ids = append(ids, id)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(ids)))
	return ids, nil
}

func (c *HitomiClient) GetCover(ctx context.Context, galleryID int) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/galleryblock/%d.html", strings.TrimRight(c.BaseURL, "/"), galleryID), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Referer", Referer)
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("hitomi: unexpected status code %d", resp.StatusCode)
	}
	return resp.Body, nil
}

func parseSearchQuery(query string) (searchQuery, error) {
	var parsed searchQuery
	terms := strings.Fields(strings.ToLower(query))
	for i := 0; i < len(terms); i++ {
		term := terms[i]
		if term == "or" || term == "-" {
			return parsed, fmt.Errorf("hitomi: invalid search term %q", term)
		}
		negative := strings.HasPrefix(term, "-")
		term = strings.ReplaceAll(strings.TrimPrefix(term, "-"), "_", " ")
		if i+1 < len(terms) && terms[i+1] == "or" {
			if negative {
				return parsed, errors.New("hitomi: OR requires positive terms")
			}
			group := []string{term}
			for i+1 < len(terms) && terms[i+1] == "or" {
				if i+2 >= len(terms) || terms[i+2] == "or" || strings.HasPrefix(terms[i+2], "-") {
					return parsed, errors.New("hitomi: OR requires positive terms on both sides")
				}
				i += 2
				group = append(group, strings.ReplaceAll(terms[i], "_", " "))
			}
			parsed.groups = append(parsed.groups, group)
		} else if negative {
			parsed.excluded = append(parsed.excluded, term)
		} else {
			parsed.groups = append(parsed.groups, []string{term})
		}
	}
	return parsed, nil
}

func nozomiPath(term string) (string, bool) {
	namespace, value, ok := strings.Cut(term, ":")
	if !ok {
		return "", false
	}
	switch namespace {
	case "female", "male":
		return "tag/" + url.PathEscape(namespace+"-"+value+"-all") + ".nozomi", true
	case "language":
		return url.PathEscape("index-"+value) + ".nozomi", true
	case "artist", "character", "series", "group", "type":
		return namespace + "/" + url.PathEscape(value+"-all") + ".nozomi", true
	default:
		return "", false
	}
}

func (c *HitomiClient) searchTerm(ctx context.Context, term, version string) (galleryIDs, error) {
	if path, ok := nozomiPath(term); ok {
		data, err := c.get(ctx, path, nil)
		if errors.Is(err, errSearchNotFound) {
			return galleryIDs{}, nil
		}
		if err != nil {
			return nil, err
		}
		return decodeGalleryIDs(data)
	}

	hash := sha256.Sum256([]byte(term))
	key := hash[:4]
	prefix := "galleriesindex/galleries." + url.PathEscape(version)
	address := uint64(0)
	visited := make(map[uint64]bool)
	for depth := 0; depth < 64; depth++ {
		if visited[address] {
			return nil, errors.New("cyclic B-tree index")
		}
		visited[address] = true
		data, err := c.get(ctx, prefix+".index", &searchRange{address, btreeNodeSize})
		if err != nil {
			return nil, err
		}
		node, err := decodeSearchNode(data)
		if err != nil {
			return nil, fmt.Errorf("decode B-tree node at %d: %w", address, err)
		}
		i := sort.Search(len(node.keys), func(i int) bool { return bytes.Compare(node.keys[i], key) >= 0 })
		if i < len(node.keys) && bytes.Equal(node.keys[i], key) {
			pointer := node.data[i]
			data, err := c.get(ctx, prefix+".data", &pointer)
			if err != nil {
				return nil, err
			}
			if len(data) < 4 || uint64(len(data)) != pointer.length || uint64(binary.BigEndian.Uint32(data[:4]))*4+4 != uint64(len(data)) {
				return nil, errors.New("invalid gallery data length or count")
			}
			return decodeGalleryIDs(data[4:])
		}
		address = node.children[i]
		if address == 0 {
			return galleryIDs{}, nil
		}
	}
	return nil, errors.New("B-tree exceeds maximum search depth")
}

func decodeGalleryIDs(data []byte) (galleryIDs, error) {
	if len(data)%4 != 0 {
		return nil, errors.New("gallery ID list is not a multiple of four bytes")
	}
	ids := make(galleryIDs, len(data)/4)
	for i := 0; i < len(data); i += 4 {
		ids[int(binary.BigEndian.Uint32(data[i:i+4]))] = struct{}{}
	}
	return ids, nil
}

type searchRange struct {
	start  uint64
	length uint64
}

type searchNode struct {
	keys     [][]byte
	data     []searchRange
	children [btreeOrder + 1]uint64
}

func decodeSearchNode(data []byte) (searchNode, error) {
	var node searchNode
	r := bytes.NewReader(data)
	read := func(value any) error { return binary.Read(r, binary.BigEndian, value) }
	var count uint32
	if err := read(&count); err != nil || count > btreeOrder {
		return node, errors.New("invalid B-tree key count")
	}
	for range count {
		var size uint32
		if err := read(&size); err != nil || size != 4 {
			return node, errors.New("invalid B-tree key size")
		}
		key := make([]byte, size)
		if _, err := io.ReadFull(r, key); err != nil {
			return node, err
		}
		if len(node.keys) > 0 && bytes.Compare(node.keys[len(node.keys)-1], key) >= 0 {
			return node, errors.New("unsorted B-tree keys")
		}
		node.keys = append(node.keys, key)
	}
	if err := read(&count); err != nil || int(count) != len(node.keys) {
		return node, errors.New("invalid B-tree data count")
	}
	for range count {
		var offset uint64
		var length uint32
		if err := read(&offset); err != nil {
			return node, err
		}
		if err := read(&length); err != nil {
			return node, err
		}
		if length < 4 || length%4 != 0 || length > maxSearchResponse {
			return node, errors.New("invalid B-tree data length")
		}
		node.data = append(node.data, searchRange{offset, uint64(length)})
	}
	if err := read(&node.children); err != nil {
		return node, err
	}
	return node, nil
}

func (c *HitomiClient) get(ctx context.Context, path string, byteRange *searchRange) ([]byte, error) {
	requestURL := strings.TrimRight(c.BaseURL, "/") + "/" + path
	if byteRange != nil && (byteRange.length == 0 || byteRange.length > maxSearchResponse || byteRange.start > ^uint64(0)-byteRange.length) {
		return nil, errors.New("invalid search byte range")
	}
	for attempt := 0; ; attempt++ {
		data, retry, err := c.getOnce(ctx, requestURL, byteRange)
		if err == nil {
			return data, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !retry || attempt+1 >= searchAttempts {
			return nil, fmt.Errorf("GET %s: %w", requestURL, err)
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 500 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (c *HitomiClient) getOnce(ctx context.Context, requestURL string, byteRange *searchRange) ([]byte, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Referer", Referer)
	if byteRange != nil {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", byteRange.start, byteRange.start+byteRange.length-1))
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, true, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, false, errSearchNotFound
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		retry := resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		return nil, retry, fmt.Errorf("HTTP %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxSearchResponse+1))
	if err != nil {
		return nil, true, err
	}
	if len(data) > maxSearchResponse {
		return nil, false, errors.New("search response exceeds 64 MiB")
	}
	if resp.StatusCode == http.StatusPartialContent {
		if byteRange == nil {
			return nil, false, errors.New("unexpected partial search response")
		}
		if err := validateSearchRange(resp.Header.Get("Content-Range"), *byteRange, len(data)); err != nil {
			return nil, false, err
		}
	} else if byteRange != nil {
		// Some servers ignore Range and send the complete file. Slice from the
		// requested offset instead of interpreting the file header as this node.
		if byteRange.start >= uint64(len(data)) {
			return nil, false, errors.New("search range starts beyond response body")
		}
		end := min(byteRange.start+byteRange.length, uint64(len(data)))
		data = data[byteRange.start:end]
	}
	return data, false, nil
}

func validateSearchRange(header string, requested searchRange, bodyLength int) error {
	span, total, ok := strings.Cut(strings.TrimPrefix(header, "bytes "), "/")
	startText, endText, hasEnd := strings.Cut(span, "-")
	start, startErr := strconv.ParseUint(startText, 10, 64)
	end, endErr := strconv.ParseUint(endText, 10, 64)
	if !strings.HasPrefix(header, "bytes ") || !ok || !hasEnd || startErr != nil || endErr != nil || start != requested.start || end < start || end-start >= requested.length || end-start+1 != uint64(bodyLength) {
		return fmt.Errorf("invalid Content-Range %q", header)
	}
	if total != "*" {
		size, err := strconv.ParseUint(total, 10, 64)
		if err != nil || size <= end {
			return fmt.Errorf("invalid Content-Range total %q", total)
		}
	}
	return nil
}
