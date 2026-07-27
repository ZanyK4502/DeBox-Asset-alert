package assetcatalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	maxLogoBytes        = 2 << 20
	maxLogoCacheEntries = 256
	maxLogoCacheBytes   = 32 << 20
	logoCacheTTL        = 24 * time.Hour
)

var allowedLogoHosts = map[string]struct{}{
	"coin-images.coingecko.com": {},
	"assets.coingecko.com":      {},
	"static.coingecko.com":      {},
	"cdn.dexscreener.com":       {},
	"dd.dexscreener.com":        {},
}

type logoProxy struct {
	client HTTPDoer

	mu         sync.Mutex
	cache      map[string]cachedLogo
	inflight   map[string]*logoCall
	cacheBytes int
}

type cachedLogo struct {
	logo      Logo
	expiresAt time.Time
}

type logoCall struct {
	done chan struct{}
	logo Logo
	err  error
}

func newLogoProxy(client HTTPDoer) (*logoProxy, error) {
	if client == nil {
		httpClient := &http.Client{Timeout: 10 * time.Second}
		httpClient.CheckRedirect = func(
			request *http.Request,
			via []*http.Request,
		) error {
			if len(via) >= 3 {
				return fmt.Errorf("too many logo redirects")
			}
			_, err := normalizeLogoURL(request.URL.String())
			return err
		}
		client = httpClient
	}
	return &logoProxy{
		client: client, cache: make(map[string]cachedLogo),
		inflight: make(map[string]*logoCall),
	}, nil
}

func (p *logoProxy) Fetch(
	ctx context.Context,
	sourceURL string,
) (Logo, error) {
	normalized, err := normalizeLogoURL(sourceURL)
	if err != nil {
		return Logo{}, err
	}
	keyHash := sha256.Sum256([]byte(normalized))
	key := hex.EncodeToString(keyHash[:])
	now := time.Now()
	p.mu.Lock()
	if item, ok := p.cache[key]; ok && now.Before(item.expiresAt) {
		logo := cloneLogo(item.logo)
		p.mu.Unlock()
		return logo, nil
	}
	if active, ok := p.inflight[key]; ok {
		p.mu.Unlock()
		select {
		case <-ctx.Done():
			return Logo{}, ctx.Err()
		case <-active.done:
			return cloneLogo(active.logo), active.err
		}
	}
	active := &logoCall{done: make(chan struct{})}
	p.inflight[key] = active
	p.mu.Unlock()

	logo, err := p.fetch(ctx, normalized)
	p.mu.Lock()
	active.logo = cloneLogo(logo)
	active.err = err
	if err == nil {
		p.evictCache(time.Now(), len(logo.Body))
		p.cache[key] = cachedLogo{
			logo: cloneLogo(logo), expiresAt: time.Now().Add(logoCacheTTL),
		}
		p.cacheBytes += len(logo.Body)
	}
	delete(p.inflight, key)
	close(active.done)
	p.mu.Unlock()
	return logo, err
}

func (p *logoProxy) fetch(ctx context.Context, sourceURL string) (Logo, error) {
	request, err := http.NewRequestWithContext(
		ctx, http.MethodGet, sourceURL, nil,
	)
	if err != nil {
		return Logo{}, ErrInvalidLogo
	}
	request.Header.Set("Accept", "image/png,image/jpeg,image/webp,image/gif")
	response, err := p.client.Do(request)
	if err != nil {
		return Logo{}, ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Logo{}, ErrUnavailable
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxLogoBytes+1))
	if err != nil || len(body) == 0 || len(body) > maxLogoBytes {
		return Logo{}, ErrInvalidLogo
	}
	contentType := strings.ToLower(strings.TrimSpace(
		strings.Split(response.Header.Get("Content-Type"), ";")[0],
	))
	sniffed := http.DetectContentType(body)
	if contentType == "" || contentType == "application/octet-stream" {
		contentType = sniffed
	}
	if !allowedImageType(contentType) || !allowedImageType(sniffed) {
		return Logo{}, ErrInvalidLogo
	}
	hash := sha256.Sum256(body)
	return Logo{
		ContentType: contentType,
		Body:        body,
		ETag:        `"` + hex.EncodeToString(hash[:]) + `"`,
	}, nil
}

func normalizeLogoURL(value string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "https" || parsed.User != nil ||
		parsed.Hostname() == "" {
		return "", ErrInvalidLogo
	}
	if port := parsed.Port(); port != "" && port != "443" {
		return "", ErrInvalidLogo
	}
	host := strings.ToLower(parsed.Hostname())
	if _, ok := allowedLogoHosts[host]; !ok {
		return "", ErrInvalidLogo
	}
	parsed.Scheme = "https"
	parsed.Host = host
	parsed.Fragment = ""
	return parsed.String(), nil
}

func allowedImageType(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "image/png", "image/jpeg", "image/webp", "image/gif":
		return true
	default:
		return false
	}
}

func (p *logoProxy) evictCache(now time.Time, incomingBytes int) {
	for key, item := range p.cache {
		if !now.Before(item.expiresAt) {
			p.cacheBytes -= len(item.logo.Body)
			delete(p.cache, key)
		}
	}
	if len(p.cache) < maxLogoCacheEntries &&
		p.cacheBytes+incomingBytes <= maxLogoCacheBytes {
		return
	}
	for len(p.cache) >= maxLogoCacheEntries ||
		p.cacheBytes+incomingBytes > maxLogoCacheBytes {
		var oldestKey string
		var oldest time.Time
		for key, item := range p.cache {
			if oldestKey == "" || item.expiresAt.Before(oldest) {
				oldestKey = key
				oldest = item.expiresAt
			}
		}
		if oldestKey == "" {
			return
		}
		p.cacheBytes -= len(p.cache[oldestKey].logo.Body)
		delete(p.cache, oldestKey)
	}
}

func cloneLogo(value Logo) Logo {
	value.Body = append([]byte(nil), value.Body...)
	return value
}
