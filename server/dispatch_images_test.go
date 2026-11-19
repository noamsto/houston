package server

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var (
	pngBytes  = []byte("\x89PNG\r\n\x1a\n....")
	jpgBytes  = []byte("\xff\xd8\xff\xe0....")
	gif87     = []byte("GIF87a....")
	gif89     = []byte("GIF89a....")
	webpBytes = []byte("RIFF\x00\x00\x00\x00WEBPVP8 ")
)

func upload(name string, data []byte) imageUpload {
	return imageUpload{Name: name, Data: base64.StdEncoding.EncodeToString(data)}
}

func TestValidateImagesAcceptsAndNames(t *testing.T) {
	tests := []struct {
		label string
		in    imageUpload
		want  string
	}{
		{"png", upload("a.png", pngBytes), "1-a.png"},
		{"jpeg bytes get .jpg", upload("a.png", jpgBytes), "1-a.jpg"},
		{"gif87a", upload("a.gif", gif87), "1-a.gif"},
		{"gif89a", upload("a.gif", gif89), "1-a.gif"},
		{"webp", upload("a.webp", webpBytes), "1-a.webp"},
		{"upper case ext kept", upload("shot.PNG", pngBytes), "1-shot.PNG"},
		{"jpeg ext kept", upload("photo.jpeg", jpgBytes), "1-photo.jpeg"},
		{"wrong ext replaced", upload("x.txt", pngBytes), "1-x.png"},
		{"no ext gets one", upload("x", pngBytes), "1-x.png"},
		{"spaces sanitized", upload("my shot.png", pngBytes), "1-my_shot.png"},
	}
	for _, tt := range tests {
		t.Run(tt.label, func(t *testing.T) {
			got, err := validateImages([]imageUpload{tt.in})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != 1 || got[0].name != tt.want {
				t.Fatalf("name = %+v, want %q", got, tt.want)
			}
		})
	}
}

func TestValidateImagesEmptyNameUsable(t *testing.T) {
	got, err := validateImages([]imageUpload{upload("", pngBytes)})
	if err != nil || len(got) != 1 {
		t.Fatalf("got %d images, err %v", len(got), err)
	}
	if !strings.HasSuffix(got[0].name, ".png") || strings.ContainsAny(got[0].name, "/ ") {
		t.Fatalf("name = %q", got[0].name)
	}
}

func TestValidateImagesNoneIsNil(t *testing.T) {
	for _, in := range [][]imageUpload{nil, {}} {
		got, err := validateImages(in)
		if got != nil || err != nil {
			t.Fatalf("got %v, %v", got, err)
		}
	}
}

func TestValidateImagesNamesUniquePerIndex(t *testing.T) {
	got, err := validateImages([]imageUpload{upload("a.png", pngBytes), upload("a.png", pngBytes)})
	if err != nil || len(got) != 2 {
		t.Fatalf("got %d images, err %v", len(got), err)
	}
	if got[0].name != "1-a.png" || got[1].name != "2-a.png" {
		t.Fatalf("names = %q, %q", got[0].name, got[1].name)
	}
	if !bytes.Equal(got[0].data, pngBytes) {
		t.Fatal("data not decoded")
	}
}

func TestValidateImagesLongNameKeepsExtension(t *testing.T) {
	got, err := validateImages([]imageUpload{upload(strings.Repeat("a", 70)+".png", pngBytes)})
	if err != nil || len(got) != 1 {
		t.Fatalf("got %d images, err %v", len(got), err)
	}
	n := got[0].name
	if len(n) > 64 || !strings.HasSuffix(n, ".png") || !strings.HasPrefix(n, "1-a") {
		t.Fatalf("name = %q (%d)", n, len(n))
	}
}

func TestValidateImagesRejects(t *testing.T) {
	mib := 1 << 20
	big := func(n int) []byte {
		return append(append([]byte{}, pngBytes...), make([]byte, n-len(pngBytes))...)
	}
	eleven := make([]imageUpload, 11)
	for i := range eleven {
		eleven[i] = upload("a.png", pngBytes)
	}
	tests := []struct {
		label string
		in    []imageUpload
		code  int
		msg   string
	}{
		{"eleven images", eleven, 400, "at most 10 images"},
		{"bad base64", []imageUpload{upload("a.png", pngBytes), {Name: "b.png", Data: "!!!"}}, 400, "image 2 is not valid base64"},
		{"over 10 MiB", []imageUpload{upload("a.png", big(10*mib+1))}, 413, "image 1 is larger than 10 MiB"},
		{"encoded length past bound never decodes", []imageUpload{{Name: "a.png", Data: strings.Repeat("!", 20*mib)}}, 413, "image 1 is larger than 10 MiB"},
		{"total over 25 MiB", []imageUpload{upload("a.png", big(9*mib)), upload("b.png", big(9*mib)), upload("c.png", big(9*mib))}, 413, "images together are larger than 25 MiB"},
		{"text", []imageUpload{upload("a.png", []byte("hello world"))}, 400, "image 1 is not a PNG, JPEG, GIF or WebP image"},
		{"empty", []imageUpload{{Name: "a.png"}}, 400, "image 1 is not a PNG, JPEG, GIF or WebP image"},
		{"wav riff", []imageUpload{upload("a.png", []byte("RIFF\x00\x00\x00\x00WAVEfmt "))}, 400, "image 1 is not a PNG, JPEG, GIF or WebP image"},
	}
	for _, tt := range tests {
		t.Run(tt.label, func(t *testing.T) {
			got, err := validateImages(tt.in)
			if err == nil {
				t.Fatalf("accepted: %v", got)
			}
			if err.code != tt.code || err.msg != tt.msg || err.field != "images" {
				t.Fatalf("err = %+v, want %d %q", err, tt.code, tt.msg)
			}
		})
	}
}

func TestWriteImages(t *testing.T) {
	dir := t.TempDir()
	paths, err := writeImages(dir, []attachImage{{"1-a.png", pngBytes}, {"2-b.jpg", jpgBytes}})
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || paths[0] != filepath.Join(dir, "1-a.png") {
		t.Fatalf("paths = %v", paths)
	}
	for i, want := range [][]byte{pngBytes, jpgBytes} {
		got, rerr := os.ReadFile(paths[i])
		if rerr != nil || !bytes.Equal(got, want) {
			t.Fatalf("file %d: %v", i, rerr)
		}
		st, serr := os.Stat(paths[i])
		if serr != nil {
			t.Fatal(serr)
		}
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %v", st.Mode().Perm())
		}
	}
}

func TestWriteImagesCollisionCleansUp(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "2-b.jpg"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := writeImages(dir, []attachImage{{"1-a.png", pngBytes}, {"2-b.jpg", jpgBytes}}); err == nil {
		t.Fatal("expected collision error")
	}
	if _, err := os.Stat(filepath.Join(dir, "1-a.png")); !os.IsNotExist(err) {
		t.Fatalf("1-a.png left behind: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "2-b.jpg")); string(got) != "old" {
		t.Fatal("pre-existing file touched")
	}
}
