package server

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	maxAttachImages       = 10
	maxAttachImageBytes   = 10 << 20
	maxAttachTotalBytes   = 25 << 20
	maxDispatchUploadBody = 36 << 20
)

// attachImage is a validated upload: decoded bytes and the file name to write.
type attachImage struct {
	name string
	data []byte
}

// validateImages checks every upload before anything is written or run, and
// names each file so agents pick an image reader by extension.
func validateImages(in []imageUpload) ([]attachImage, *dispatchError) {
	if len(in) == 0 {
		return nil, nil
	}
	fail := func(code int, format string, args ...any) *dispatchError {
		return &dispatchError{field: "images", code: code, msg: fmt.Sprintf(format, args...)}
	}
	if len(in) > maxAttachImages {
		return nil, fail(http.StatusBadRequest, "at most %d images", maxAttachImages)
	}
	out := make([]attachImage, 0, len(in))
	total := 0
	for i, img := range in {
		n := i + 1
		// The slack covers a partial last group; the exact size is checked
		// after decoding.
		if base64.StdEncoding.DecodedLen(len(img.Data)) > maxAttachImageBytes+2 {
			return nil, fail(http.StatusRequestEntityTooLarge, "image %d is larger than 10 MiB", n)
		}
		data, err := base64.StdEncoding.DecodeString(img.Data)
		if err != nil {
			return nil, fail(http.StatusBadRequest, "image %d is not valid base64", n)
		}
		if len(data) > maxAttachImageBytes {
			return nil, fail(http.StatusRequestEntityTooLarge, "image %d is larger than 10 MiB", n)
		}
		ext, ok := imageExt(data)
		if !ok {
			return nil, fail(http.StatusBadRequest, "image %d is not a PNG, JPEG, GIF or WebP image", n)
		}
		total += len(data)
		if total > maxAttachTotalBytes {
			return nil, fail(http.StatusRequestEntityTooLarge, "images together are larger than 25 MiB")
		}
		out = append(out, attachImage{name: attachImageName(n, img.Name, ext), data: data})
	}
	return out, nil
}

// attachImageName gives the file the detected type's extension unless the
// upload's own already matches, then prefixes the 1-based index so names in
// one request never collide.
func attachImageName(n int, name, ext string) string {
	s := safeImageName(name)
	cur := filepath.Ext(s)
	matches := strings.EqualFold(cur, ext) || (ext == ".jpg" && strings.EqualFold(cur, ".jpeg"))
	if !matches {
		s = strings.TrimSuffix(s, cur) + ext
	}
	return safeImageName(strconv.Itoa(n) + "-" + s)
}

// imageExt detects the type by the same signatures dispatch uses.
func imageExt(data []byte) (string, bool) {
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return ".png", true
	case bytes.HasPrefix(data, []byte("\xff\xd8\xff")):
		return ".jpg", true
	case bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")):
		return ".gif", true
	case len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return ".webp", true
	}
	return "", false
}

// writeImages writes each image into dir (never overwriting) and returns the
// absolute paths in order. On any failure it removes what it wrote.
func writeImages(dir string, imgs []attachImage) ([]string, error) {
	paths := make([]string, 0, len(imgs))
	for _, img := range imgs {
		p := filepath.Join(dir, img.name)
		if err := writeNewFile(p, img.data); err != nil {
			for _, w := range paths {
				_ = os.Remove(w)
			}
			return nil, err
		}
		paths = append(paths, p)
	}
	return paths, nil
}

func writeNewFile(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // dir is houston's own temp/state dir; the name passed safeImageName
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}
