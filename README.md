# favicon

[![Go Reference](https://pkg.go.dev/badge/go.lucor.dev/favicon.svg)](https://pkg.go.dev/go.lucor.dev/favicon)
[![Dependencies](https://badges.api.sourcespotter.com/deps?package=go.lucor.dev%2Ffavicon)](https://sourcespotter.com/deps/?package=go.lucor.dev%2Ffavicon)

Find and download a website's favicon with Go.

```sh
go get go.lucor.dev/favicon
```

Pass a page URL to `Download`. URLs without a scheme use HTTPS; HTTP URLs are
not supported.

```go
import (
    "context"
    "net/url"

    "go.lucor.dev/favicon"
)

func discover(ctx context.Context) error {
    page, err := url.Parse("https://example.com")
    if err != nil {
        return err
    }

    result, err := favicon.Download(ctx, page, favicon.Options{})
    if err != nil {
        return err
    }

    if result.Icon.Raster != nil {
        // Use the decoded image.
    }
    return nil
}
```

The package supports PNG, JPEG, ICO, GIF, WebP, and SVG. SVG is returned as-is;
it is not rendered or sanitized. Requests have a five-second timeout by default.

## CLI

The diagnostic command saves raster icons as PNG and SVG icons as SVG:

```sh
go run ./cmd/favicon -debug -output favicon.png https://example.com
```

`-output` is optional. The default filename is `favicon.png` for raster icons
and `favicon.svg` for SVG. Diagnostic logs go to standard error.

## License

MIT. See [LICENSE](LICENSE).
