package favicon

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func pngData(t *testing.T, size int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, size, size))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestDownloadDiscoversRelativeIconAndRedirectURL(t *testing.T) {
	icon := pngData(t, 16)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			http.Redirect(w, r, "/dir/page", http.StatusFound)
		case "/dir/page":
			if r.Header.Get("If-None-Match") != "" || r.Header.Get("If-Modified-Since") != "" {
				t.Error("discovery forwarded an unrelated validator")
			}
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<html><head><link rel="apple-touch-icon" href="icons/icon.png"></head></html>`))
		case "/dir/icons/icon.png":
			http.Redirect(w, r, "/final.png", http.StatusFound)
		case "/final.png":
			_, _ = w.Write(icon)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL + "/start")
	result, err := Download(context.Background(), u, Options{Client: server.Client(), ETag: `"page"`})
	if err != nil {
		t.Fatal(err)
	}
	if result.URL != server.URL+"/final.png" || result.Icon.Format != "png" {
		t.Fatalf("result = %+v", result)
	}
}

func TestDownloadFallbackAndSelection(t *testing.T) {
	icon := pngData(t, 3)
	for _, tc := range []struct{ name, html, want string }{
		{"fallback", `<html><head></head></html>`, "/favicon.ico"},
		{"reject unsupported discovered scheme", `<head><link rel="icon" href="file:///etc/passwd"></head>`, "/favicon.ico"},
		{"png before larger other", `<head><link rel="icon" href="large.ico" sizes="512x512"><link rel="icon" href="small.png" type="image/png" sizes="16x16"></head>`, "/small.png"},
		{"largest png", `<head><link rel="icon" href="small.png" sizes="16x16"><link rel="icon" href="large.png" sizes="64x64"></head>`, "/large.png"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/" {
					_, _ = w.Write([]byte(tc.html))
					return
				}
				if r.URL.Path == tc.want {
					_, _ = w.Write(icon)
					return
				}
				http.NotFound(w, r)
			}))
			defer server.Close()
			u, _ := url.Parse(server.URL)
			result, err := Download(context.Background(), u, Options{Client: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasSuffix(result.URL, tc.want) {
				t.Fatalf("URL = %q, want suffix %q", result.URL, tc.want)
			}
		})
	}
}

func TestUserAgent(t *testing.T) {
	for _, tc := range []struct{ input, want string }{{"custom-agent", "custom-agent"}, {"", defaultUserAgent}} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if got := r.UserAgent(); got != tc.want {
				t.Errorf("User-Agent = %q, want %q", got, tc.want)
			}
			_, _ = w.Write(pngData(t, 1))
		}))
		_, err := DownloadURL(context.Background(), server.URL, Options{Client: server.Client(), UserAgent: tc.input})
		server.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestConditionalRequests(t *testing.T) {
	const modified = "Tue, 06 Oct 2026 12:00:00 GMT"
	for _, tc := range []struct {
		name               string
		status             int
		etag, lastModified string
		responseETag       string
		responseDate       string
		wantErr            bool
	}{
		{name: "etag not changed", status: http.StatusNotModified, etag: `"v1"`},
		{name: "last modified not changed", status: http.StatusNotModified, lastModified: modified},
		{name: "updated validators", status: http.StatusOK, etag: `"old"`, lastModified: modified, responseETag: `"new"`, responseDate: modified},
		{name: "unsolicited 304", status: http.StatusNotModified, wantErr: true},
		{name: "unexpected", status: http.StatusForbidden, etag: `"v1"`, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("If-None-Match") != tc.etag || r.Header.Get("If-Modified-Since") != tc.lastModified {
					t.Errorf("validators = %v", r.Header)
				}
				w.Header().Set("ETag", tc.responseETag)
				w.Header().Set("Last-Modified", tc.responseDate)
				w.WriteHeader(tc.status)
				if tc.status == http.StatusOK {
					_, _ = w.Write(pngData(t, 1))
				}
			}))
			defer server.Close()
			result, err := DownloadURL(context.Background(), server.URL, Options{Client: server.Client(), ETag: tc.etag, LastModified: tc.lastModified})
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if tc.status == http.StatusOK {
				if err != nil || result.NotModified || result.ETag != tc.responseETag || result.LastModified != tc.responseDate || result.Icon.Raster == nil {
					t.Fatalf("updated result=%+v err=%v", result, err)
				}
				return
			}
			if err != nil || !result.NotModified {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if result.ETag != tc.etag || result.LastModified != tc.lastModified {
				t.Fatalf("validators not retained: %+v", result)
			}
		})
	}
}

func TestDecodeFormats(t *testing.T) {
	var jpegBuf, gifBuf bytes.Buffer
	if err := jpeg.Encode(&jpegBuf, image.NewRGBA(image.Rect(0, 0, 1, 1)), nil); err != nil {
		t.Fatal(err)
	}
	if err := gif.Encode(&gifBuf, image.NewRGBA(image.Rect(0, 0, 1, 1)), nil); err != nil {
		t.Fatal(err)
	}
	webp, err := base64.StdEncoding.DecodeString("UklGRiIAAABXRUJQVlA4IBYAAAAwAQCdASoBAAEADsD+JaQAA3AAAAAA")
	if err != nil {
		t.Fatal(err)
	}
	pngIcon := pngData(t, 1)
	ico := make([]byte, 22+len(pngIcon))
	binary.LittleEndian.PutUint16(ico[2:4], 1)
	binary.LittleEndian.PutUint16(ico[4:6], 1)
	ico[6], ico[7] = 1, 1
	binary.LittleEndian.PutUint16(ico[10:12], 1)
	binary.LittleEndian.PutUint16(ico[12:14], 32)
	binary.LittleEndian.PutUint32(ico[14:18], uint32(len(pngIcon)))
	binary.LittleEndian.PutUint32(ico[18:22], 22)
	copy(ico[22:], pngIcon)
	for name, data := range map[string][]byte{"png": pngData(t, 1), "jpeg": jpegBuf.Bytes(), "gif": gifBuf.Bytes(), "webp": webp, "ico": ico} {
		t.Run(name, func(t *testing.T) {
			icon, err := Decode(data)
			if err != nil {
				t.Fatal(err)
			}
			if icon.Raster == nil || icon.Format != name {
				t.Fatalf("icon=%+v", icon)
			}
		})
	}
}

func TestDecodeSVGValidation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		data    string
		wantSVG bool
	}{
		{"svg", `<svg xmlns="http://www.w3.org/2000/svg"><path/></svg>`, true},
		{"xml declaration", `<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"/>`, true},
		{"unrelated xml", `<?xml version="1.0"?><feed/>`, false},
		{"unrelated namespace", `<other:svg xmlns:other="urn:example"/>`, false},
		{"second root", `<svg/><other/>`, false},
		{"text after root", `<svg/>unexpected`, false},
		{"directive after root", `<svg/><!DOCTYPE other>`, false},
		{"malformed", `<svg><path></svg>`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			icon, err := Decode([]byte(tc.data))
			if tc.wantSVG {
				if err != nil || !bytes.Equal(icon.SVG, []byte(tc.data)) {
					t.Fatalf("icon=%+v err=%v", icon, err)
				}
			} else if err == nil {
				t.Fatal("expected invalid data error")
			}
		})
	}
}

func TestRejectURLAndRedirectSchemes(t *testing.T) {
	for _, raw := range []string{"ftp://example.com/icon", "http:///missing", "://broken"} {
		if _, err := DownloadURL(context.Background(), raw, Options{}); err == nil {
			t.Errorf("DownloadURL(%q) succeeded", raw)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "file:///tmp/x")
		w.WriteHeader(http.StatusFound)
	}))
	defer server.Close()
	if _, err := DownloadURL(context.Background(), server.URL, Options{Client: server.Client()}); err == nil {
		t.Fatal("unsafe redirect accepted")
	}
}

func TestLimitsAndCancellation(t *testing.T) {
	oversized := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("x"), maxIconBytes+1))
	}))
	defer oversized.Close()
	if _, err := DownloadURL(context.Background(), oversized.URL, Options{Client: oversized.Client()}); err == nil {
		t.Fatal("oversized response accepted")
	}

	htmlServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(append([]byte(`<head></head>`), bytes.Repeat([]byte("x"), maxHTMLBytes)...))
	}))
	defer htmlServer.Close()
	u, _ := url.Parse(htmlServer.URL)
	if _, err := Download(context.Background(), u, Options{Client: htmlServer.Client()}); err == nil {
		t.Fatal("oversized HTML accepted")
	}

	if _, err := Decode(pngData(t, maxIconDimension+1)); err == nil {
		t.Fatal("excessive dimensions accepted")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := DownloadURL(cancelled, oversized.URL, Options{Client: oversized.Client()}); err == nil {
		t.Fatal("cancelled request succeeded")
	}
}

func TestDownloadDoesNotMutateInputURLOrClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(pngData(t, 1)) }))
	defer server.Close()
	u, _ := url.Parse(strings.TrimPrefix(server.URL, "http:"))
	before := *u
	client := server.Client()
	checkRedirect := client.CheckRedirect
	if _, err := Download(context.Background(), u, Options{Client: client}); err == nil {
		t.Fatal("scheme-less URL should default to HTTPS and fail against HTTP server")
	}
	if *u != before {
		t.Fatalf("URL mutated: got %+v want %+v", *u, before)
	}
	if client.CheckRedirect != nil && checkRedirect == nil {
		t.Fatal("client mutated")
	}
}
