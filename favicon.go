// Package favicon discovers, downloads, and decodes website favicons.
package favicon

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	_ "github.com/fyne-io/image/ico"
	_ "golang.org/x/image/webp"
	"golang.org/x/net/html"
)

const (
	httpClientTimeout = 5 * time.Second
	maxHTMLBytes      = 1 << 20
	maxIconBytes      = 2 << 20
	maxIconDimension  = 4096
	maxIconPixels     = 16 << 20
	defaultUserAgent  = "go.lucor.dev/favicon"
)

// Options configures HTTP requests made while discovering or downloading an icon.
type Options struct {
	// Client is the HTTP client to use. A nil client uses a five-second timeout.
	Client *http.Client
	// UserAgent overrides the neutral default User-Agent.
	UserAgent string
	// ETag and LastModified validate the resource passed to DownloadURL.
	// Download discovers an icon and does not use these validators.
	ETag         string
	LastModified string
}

// DownloadResult describes a downloaded icon and its HTTP cache validators.
type DownloadResult struct {
	Icon         Icon
	URL          string
	ETag         string
	LastModified string
	// NotModified indicates successful validation; Icon is empty in this case.
	NotModified bool
}

// Icon contains either a decoded raster image or the original SVG bytes.
// Exactly one field is set for a valid icon. SVG bytes are not sanitized.
type Icon struct {
	// Raster is the decoded image for raster formats.
	Raster image.Image
	// SVG is the original source for SVG images.
	SVG []byte
	// Format is the detected format, such as png, ico, or svg.
	Format string
	// Size is the raster image size and is zero for SVG.
	Size image.Point
}

// Download discovers and downloads the best icon linked from a page.
func Download(ctx context.Context, u *url.URL, opts Options) (DownloadResult, error) {
	if u == nil {
		return DownloadResult{}, fmt.Errorf("favicon URL is nil")
	}
	pageURL := *u
	if pageURL.Scheme == "" {
		pageURL.Scheme = "https"
	}
	if err := validateURL(&pageURL); err != nil {
		return DownloadResult{}, fmt.Errorf("invalid page URL: %w", err)
	}
	client := requestClient(opts.Client)
	iconData, baseURL, err := findFavicon(ctx, client, &pageURL, opts.UserAgent)
	if err != nil {
		return DownloadResult{}, fmt.Errorf("find favicon: %w", err)
	}
	iconURL := iconData.MakeRawURL(baseURL)
	return downloadURL(ctx, client, iconURL, Options{UserAgent: opts.UserAgent})
}

// DownloadURL downloads and decodes an icon from a direct URL.
func DownloadURL(ctx context.Context, rawURL string, opts Options) (DownloadResult, error) {
	client := requestClient(opts.Client)
	return downloadURL(ctx, client, rawURL, opts)
}

// requestClient copies the client and validates every redirect destination.
func requestClient(client *http.Client) *http.Client {
	if client == nil {
		return &http.Client{Timeout: httpClientTimeout}
	}
	copy := *client
	previous := client.CheckRedirect
	copy.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if err := validateURL(req.URL); err != nil {
			return fmt.Errorf("invalid redirect URL: %w", err)
		}
		if previous != nil {
			if err := previous(req, via); err != nil {
				return err
			}
			return validateURL(req.URL)
		}
		if len(via) >= 10 {
			return http.ErrUseLastResponse
		}
		return nil
	}
	return &copy
}

// validateURL accepts only well-formed HTTP and HTTPS URLs with a host.
func validateURL(u *url.URL) error {
	if u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return fmt.Errorf("URL must use HTTP or HTTPS and include a host")
	}
	if _, err := url.ParseRequestURI(u.String()); err != nil {
		return fmt.Errorf("malformed URL: %w", err)
	}
	return nil
}

// downloadURL fetches an icon, handles conditional responses, and decodes its body.
func downloadURL(ctx context.Context, client *http.Client, rawURL string, opts Options) (DownloadResult, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return DownloadResult{}, fmt.Errorf("invalid favicon URL: %w", err)
	}
	if err := validateURL(u); err != nil {
		return DownloadResult{}, fmt.Errorf("invalid favicon URL: %w", err)
	}
	res, err := makeRequest(ctx, client, u.String(), opts)
	if err != nil {
		return DownloadResult{}, fmt.Errorf("download favicon: %w", err)
	}
	defer res.Body.Close()
	result := DownloadResult{URL: res.Request.URL.String(), ETag: res.Header.Get("ETag"), LastModified: res.Header.Get("Last-Modified")}
	if res.StatusCode == http.StatusNotModified {
		result.NotModified = true
		if result.ETag == "" {
			result.ETag = opts.ETag
		}
		if result.LastModified == "" {
			result.LastModified = opts.LastModified
		}
		return result, nil
	}
	data, err := readLimited(res.Body, maxIconBytes)
	if err != nil {
		return DownloadResult{}, fmt.Errorf("read favicon: %w", err)
	}
	icon, err := Decode(data)
	if err != nil {
		contentType := res.Header.Get("Content-Type")
		if contentType == "" {
			contentType = "not provided"
		}
		return DownloadResult{}, fmt.Errorf("decode favicon with Content-Type %q: %w", contentType, err)
	}
	result.Icon = icon
	return result, nil
}

// Decode validates and decodes bounded favicon data.
func Decode(data []byte) (Icon, error) {
	if len(data) > maxIconBytes {
		return Icon{}, fmt.Errorf("favicon exceeds %d-byte limit", maxIconBytes)
	}
	if len(data) == 0 {
		return Icon{}, fmt.Errorf("favicon is empty")
	}
	if isSVG(data) {
		return Icon{SVG: bytes.Clone(data), Format: "svg"}, nil
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return Icon{}, fmt.Errorf("unsupported or invalid image format: %w", err)
	}
	if !validDimensions(config.Width, config.Height) {
		return Icon{}, fmt.Errorf("%s image dimensions %dx%d are outside the supported limit", format, config.Width, config.Height)
	}
	decoded, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return Icon{}, fmt.Errorf("decode %s image: %w", format, err)
	}
	bounds := decoded.Bounds()
	if !validDimensions(bounds.Dx(), bounds.Dy()) {
		return Icon{}, fmt.Errorf("%s image dimensions %dx%d are outside the supported limit", format, bounds.Dx(), bounds.Dy())
	}
	return Icon{Raster: decoded, Format: format, Size: bounds.Size()}, nil
}

// validDimensions reports whether raster dimensions fit the package limits.
func validDimensions(width, height int) bool {
	return width > 0 && height > 0 && width <= maxIconDimension && height <= maxIconDimension && int64(width)*int64(height) <= maxIconPixels
}

// isSVG reports whether data is well-formed XML with an SVG root element.
func isSVG(data []byte) bool {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	depth := 0
	rootSeen := false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return rootSeen
		}
		if err != nil {
			return false
		}
		switch value := token.(type) {
		case xml.StartElement:
			if depth == 0 {
				if rootSeen || value.Name.Local != "svg" || value.Name.Space != "" && value.Name.Space != "http://www.w3.org/2000/svg" {
					return false
				}
				rootSeen = true
			}
			depth++
		case xml.EndElement:
			depth--
		case xml.CharData:
			if depth == 0 && strings.TrimSpace(string(value)) != "" {
				return false
			}
		case xml.Directive:
			if depth == 0 && rootSeen {
				return false
			}
		case xml.ProcInst, xml.Comment:
		}
	}
}

// makeRequest sends a validated GET request and rejects unexpected status codes.
func makeRequest(ctx context.Context, client *http.Client, rawURL string, opts Options) (*http.Response, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse URL: %w", err)
	}
	if err := validateURL(u); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create HTTP request: %w", err)
	}
	req.Header.Set("Accept", "image/*, text/html;q=0.9, */*;q=0.8")
	userAgent := opts.UserAgent
	if userAgent == "" {
		userAgent = defaultUserAgent
	}
	req.Header.Set("User-Agent", userAgent)
	if opts.ETag != "" {
		req.Header.Set("If-None-Match", opts.ETag)
	}
	if opts.LastModified != "" {
		req.Header.Set("If-Modified-Since", opts.LastModified)
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch URL: %w", err)
	}
	conditional := opts.ETag != "" || opts.LastModified != ""
	if res.StatusCode != http.StatusOK && (!conditional || res.StatusCode != http.StatusNotModified) {
		defer res.Body.Close()
		return nil, fmt.Errorf("HTTP %d: %s", res.StatusCode, res.Status)
	}
	return res, nil
}

// findFavicon parses the page and selects a valid linked icon or the standard fallback.
func findFavicon(ctx context.Context, client *http.Client, u *url.URL, userAgent string) (*faviconData, *url.URL, error) {
	res, err := makeRequest(ctx, client, u.String(), Options{UserAgent: userAgent})
	if err != nil {
		return nil, nil, err
	}
	defer res.Body.Close()
	page, err := readLimited(res.Body, maxHTMLBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("homepage HTML exceeds %d-byte limit: %w", maxHTMLBytes, err)
	}
	baseURL := res.Request.URL
	favicons := parseHeadSection(bytes.NewReader(page))
	if len(favicons) == 0 {
		return &faviconData{href: "/favicon.ico", rel: "icon", mime: "image/x-icon"}, baseURL, nil
	}
	for i := len(favicons) - 1; i >= 0; i-- {
		candidateURL, err := url.Parse(favicons[i].href)
		if err != nil {
			favicons = append(favicons[:i], favicons[i+1:]...)
			continue
		}
		resolved := baseURL.ResolveReference(candidateURL)
		if validateURL(resolved) != nil {
			favicons = append(favicons[:i], favicons[i+1:]...)
		}
	}
	if len(favicons) == 0 {
		return &faviconData{href: "/favicon.ico", rel: "icon", mime: "image/x-icon"}, baseURL, nil
	}
	return findBetterSize(favicons), baseURL, nil
}

// readLimited reads at most limit bytes and reports when more data is available.
func readLimited(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("response exceeds %d-byte limit", limit)
	}
	return data, nil
}

// parseHeadSection collects supported icon links from an HTML document's head.
func parseHeadSection(r io.Reader) []*faviconData {
	tokenizer := html.NewTokenizer(r)
	favicons := make([]*faviconData, 0)
	for {
		switch tokenizer.Next() {
		case html.ErrorToken:
			return favicons
		case html.StartTagToken, html.SelfClosingTagToken:
			token := tokenizer.Token()
			if token.Data == "link" {
				if icon := getFaviconData(token); icon != nil {
					favicons = append(favicons, icon)
				}
			}
		case html.EndTagToken:
			if tokenizer.Token().Data == "head" {
				return favicons
			}
		}
	}
}

type faviconData struct {
	href, rel string
	size      int
	mime      string
}

// MakeRawURL resolves the icon reference against its page URL.
func (f *faviconData) MakeRawURL(u *url.URL) string {
	reference, err := url.Parse(f.href)
	if err != nil {
		return ""
	}
	return u.ResolveReference(reference).String()
}

// getFaviconData extracts supported icon metadata from an HTML link token.
func getFaviconData(token html.Token) *faviconData {
	var rel, href, mimeType string
	var size int
	for _, attr := range token.Attr {
		switch attr.Key {
		case "rel":
			for _, relation := range strings.Fields(strings.ToLower(attr.Val)) {
				if relation == "icon" || relation == "apple-touch-icon" {
					rel = attr.Val
					break
				}
			}
		case "href":
			href = strings.TrimSpace(attr.Val)
		case "sizes":
			value, _, found := strings.Cut(strings.ToLower(attr.Val), "x")
			if found {
				size, _ = strconv.Atoi(value)
			}
		case "type":
			mimeType = attr.Val
		}
	}
	if rel == "" || href == "" {
		return nil
	}
	return &faviconData{rel: rel, href: href, mime: mimeType, size: size}
}

// findBetterSize prefers PNG icons, then the largest declared size.
func findBetterSize(favicons []*faviconData) *faviconData {
	best := favicons[0]
	for _, candidate := range favicons[1:] {
		candidatePNG, bestPNG := isPNG(candidate), isPNG(best)
		if candidatePNG && !bestPNG || candidatePNG == bestPNG && candidate.size > best.size {
			best = candidate
		}
	}
	return best
}

// isPNG reports whether the link declares or names a PNG icon.
func isPNG(favicon *faviconData) bool {
	if strings.EqualFold(favicon.mime, "image/png") {
		return true
	}
	reference, err := url.Parse(favicon.href)
	return err == nil && strings.EqualFold(path.Ext(reference.Path), ".png")
}
