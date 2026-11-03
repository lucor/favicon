# favicon

`go.lucor.dev/favicon` discovers website favicon links, downloads the selected
icon, and decodes raster images. It supports PNG, JPEG, ICO, GIF, and WebP
raster images, plus SVG bytes without rendering them.

Install it with:

```console
go get go.lucor.dev/favicon
```

Discover an icon from a page:

```go
import (
 "context"
 "net/url"

 "go.lucor.dev/favicon"
)

func discover(ctx context.Context) error {
    page, err := url.Parse("https://example.com/")
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

Download a known icon and validate it on a later request:

```go
previous := result // Keep the result from an earlier request.
result, err := favicon.DownloadURL(ctx, iconURL, favicon.Options{
 ETag:         previous.ETag,
 LastModified: previous.LastModified,
})
if err != nil {
 return err
}
if result.NotModified {
 // Keep the previously downloaded icon.
}
```

Raster results are available in `Icon.Raster`, with their format and dimensions
in `Format` and `Size`. SVG results preserve the original bytes in `Icon.SVG`;
SVG input must be well-formed XML with a single SVG root element. The library
does not render or sanitize SVG content.

Requests use a five-second timeout by default. HTML is limited to 1 MiB, icon
responses to 2 MiB, and raster images to 4096 pixels per dimension and
16,777,216 pixels total. A custom HTTP client and User-Agent can be supplied
in `Options`.

ICO parsing is delegated to the image decoder. Dimension checks use its
reported configuration and decoded result; they do not bound the decoder's
internal allocations or validate every embedded image before decoding.

The library does not provide persistent caching, resizing, image rendering, or
application-specific storage.
