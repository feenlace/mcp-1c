package tools

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func gridFixture(t *testing.T, format string) (string, []byte) {
	t.Helper()
	root := t.TempDir()
	src := image.NewRGBA(image.Rect(0, 0, 64, 48))
	for y := 0; y < 48; y++ {
		for x := 0; x < 64; x++ {
			src.SetRGBA(x, y, color.RGBA{R: uint8(x * 3), G: uint8(y * 4), B: 80, A: 255})
		}
	}
	var b bytes.Buffer
	var err error
	if format == "jpeg" {
		err = jpeg.Encode(&b, src, &jpeg.Options{Quality: 90})
	} else {
		err = png.Encode(&b, src)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "input"), b.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return root, b.Bytes()
}

func TestImageGridPixelsLabelsAndPreservedOriginal(t *testing.T) {
	for _, format := range []string{"png", "jpeg"} {
		t.Run(format, func(t *testing.T) {
			root, original := gridFixture(t, format)
			r, err := CreateImageGrid(context.Background(), root, ImageGridRequest{InputPath: "input", OutputPath: "grid.png", Spacing: 16})
			if err != nil || r.Width != 112 || r.Height != 72 || r.Columns != 4 || r.Rows != 3 || r.InputFormat != format || r.InputSHA256 != localHash(original) || r.Orientation != "stored_pixels_no_exif_rotation" {
				t.Fatalf("grid: %+v %v", r, err)
			}
			encoded, err := os.ReadFile(filepath.Join(root, "grid.png"))
			if err != nil {
				t.Fatal(err)
			}
			if localHash(encoded) != r.OutputSHA256 {
				t.Fatal("output hash mismatch")
			}
			output, err := png.Decode(bytes.NewReader(encoded))
			if err != nil {
				t.Fatal(err)
			}
			if output.Bounds() != image.Rect(0, 0, 112, 72) {
				t.Fatal("wrong output dimensions")
			}
			if color.RGBAModel.Convert(output.At(24+16, 12+1)) != (color.RGBA{220, 30, 30, 255}) {
				t.Fatal("vertical line missing")
			}
			if color.RGBAModel.Convert(output.At(25, 12+16)) != (color.RGBA{220, 30, 30, 255}) {
				t.Fatal("horizontal line missing")
			}
			if color.RGBAModel.Convert(output.At(24, 2)) != (color.RGBA{0, 0, 0, 255}) {
				t.Fatal("column number 1 missing")
			}
			if color.RGBAModel.Convert(output.At(18, 12)) != (color.RGBA{0, 0, 0, 255}) {
				t.Fatal("row number 1 missing")
			}
			source, _, _ := image.Decode(bytes.NewReader(original))
			if color.RGBAModel.Convert(output.At(29, 17)) != color.RGBAModel.Convert(source.At(5, 5)) {
				t.Fatal("non-grid source pixel changed")
			}
			after, _ := os.ReadFile(filepath.Join(root, "input"))
			if !bytes.Equal(after, original) {
				t.Fatal("original changed")
			}
			if _, err := CreateImageGrid(context.Background(), root, ImageGridRequest{InputPath: "input", OutputPath: "grid.png", Spacing: 16}); err == nil {
				t.Fatal("output collision accepted")
			}
		})
	}
}

func TestImageGridBoundsPathsAndCancellation(t *testing.T) {
	root, original := gridFixture(t, "png")
	for _, r := range []ImageGridRequest{{"input", "out", 0}, {"input", "out", -1}, {"input", "out", 8193}, {"input", "input", 16}, {"input", "../outside", 16}, {"missing", "out", 16}} {
		if _, err := CreateImageGrid(context.Background(), root, r); err == nil {
			t.Fatalf("accepted %+v", r)
		}
	}
	_ = os.WriteFile(filepath.Join(root, "bad"), []byte("not an image"), 0600)
	if _, err := CreateImageGrid(context.Background(), root, ImageGridRequest{"bad", "out", 16}); err == nil {
		t.Fatal("bad image accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := CreateImageGrid(ctx, root, ImageGridRequest{"input", "out", 16}); err == nil {
		t.Fatal("cancellation ignored")
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "escape")); err == nil {
		if _, err := CreateImageGrid(context.Background(), root, ImageGridRequest{"input", "escape/out", 16}); err == nil {
			t.Fatal("symlink escape accepted")
		}
	}
	after, _ := os.ReadFile(filepath.Join(root, "input"))
	if !bytes.Equal(after, original) {
		t.Fatal("refusals changed source")
	}
}

func TestImageGridRefusesOversizedPNGBeforeDecode(t *testing.T) {
	for _, size := range [][2]uint32{{8193, 1}, {8192, 8192}} {
		root := t.TempDir()
		// A valid IHDR is enough for DecodeConfig, but contains no pixel payload.
		data := append([]byte(nil), []byte("\x89PNG\r\n\x1a\n")...)
		chunk := make([]byte, 25)
		binary.BigEndian.PutUint32(chunk[0:4], 13)
		copy(chunk[4:8], "IHDR")
		binary.BigEndian.PutUint32(chunk[8:12], size[0])
		binary.BigEndian.PutUint32(chunk[12:16], size[1])
		chunk[16], chunk[17] = 8, 6
		binary.BigEndian.PutUint32(chunk[21:25], crc32.ChecksumIEEE(chunk[4:21]))
		data = append(data, chunk...)
		if err := os.WriteFile(filepath.Join(root, "huge.png"), data, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := CreateImageGrid(context.Background(), root, ImageGridRequest{"huge.png", "out.png", 16}); err == nil {
			t.Fatal("oversized image header accepted")
		}
		if _, err := os.Stat(filepath.Join(root, "out.png")); !os.IsNotExist(err) {
			t.Fatal("rejected image created output")
		}
	}
}
