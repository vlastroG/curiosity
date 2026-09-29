package book

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Fetcher скачивает книги с зеркала Gutenberg и держит их в кэше.
//
// Правила Gutenberg: автоматические запросы к www.gutenberg.org запрещены,
// для программ -- зеркала и пауза между запросами. Поэтому книга скачивается
// один раз, дальше берётся из кэша; положить файл в кэш руками тоже можно --
// так работает офлайн.
type Fetcher struct {
	Mirror string // https://gutenberg.pglaf.org
	Dir    string // кэш: <Dir>/pg74.txt
	Pause  time.Duration
	Client *http.Client

	last time.Time
}

// CachePath -- где лежит (или будет лежать) файл книги.
func (f *Fetcher) CachePath(n int) string {
	return filepath.Join(f.Dir, fmt.Sprintf("pg%d.txt", n))
}

// Get возвращает содержимое книги и признак того, что она взята из кэша.
func (f *Fetcher) Get(ctx context.Context, n int) (raw []byte, cached bool, err error) {
	path := f.CachePath(n)
	if raw, err := os.ReadFile(path); err == nil {
		return raw, true, nil
	}

	var errs []error
	for _, url := range MirrorURLs(f.Mirror, n) {
		raw, err = f.download(ctx, url)
		if err == nil {
			if err := os.MkdirAll(f.Dir, 0o755); err != nil {
				return nil, false, err
			}
			if err := os.WriteFile(path, raw, 0o644); err != nil {
				return nil, false, err
			}
			return raw, false, nil
		}
		errs = append(errs, err)
		if ctx.Err() != nil {
			break
		}
	}
	return nil, false, fmt.Errorf("книга %d не скачалась: %w (можно положить файл вручную в %s)",
		n, errors.Join(errs...), path)
}

func (f *Fetcher) download(ctx context.Context, url string) ([]byte, error) {
	if wait := f.Pause - time.Since(f.last); wait > 0 && !f.last.IsZero() {
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f.last = time.Now()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "doc-index/1.0 (personal search index)")
	client := f.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil {
		return nil, err
	}
	if !strings.Contains(string(raw), "PROJECT GUTENBERG EBOOK") {
		return nil, fmt.Errorf("%s: это не текст книги Gutenberg", url)
	}
	return raw, nil
}

// MirrorURLs -- адреса текста книги на зеркале. Зеркала раскладывают книги
// по цифрам номера: 74 → 7/74/74-0.txt, 1342 → 1/3/4/1342/1342-0.txt.
func MirrorURLs(mirror string, n int) []string {
	digits := strconv.Itoa(n)
	var dir []string
	if len(digits) == 1 {
		dir = []string{"0"}
	} else {
		for _, d := range digits[:len(digits)-1] {
			dir = append(dir, string(d))
		}
	}
	base := strings.TrimRight(mirror, "/") + "/" + strings.Join(dir, "/") + "/" + digits + "/"
	return []string{base + digits + "-0.txt", base + digits + ".txt"}
}
