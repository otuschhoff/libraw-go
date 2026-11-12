// SPDX-License-Identifier: LGPL-2.1-only

package libraw_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/png"
	"io"
	"os"

	libraw "github.com/otuschhoff/libraw-go"
)

func ExampleOpen() {
	data := []byte("not an ORF")
	_, err := libraw.Open(bytes.NewReader(data), int64(len(data)), libraw.OpenOptions{})
	fmt.Println(errors.Is(err, libraw.ErrNotORF))
	// Output: true
}

// The caller keeps input open while File operations are in progress.
func ExampleFile_Metadata() {
	read := func(path string) (libraw.Metadata, libraw.Capabilities, error) {
		input, err := os.Open(path)
		if err != nil {
			return libraw.Metadata{}, libraw.Capabilities{}, err
		}
		defer input.Close()
		info, err := input.Stat()
		if err != nil {
			return libraw.Metadata{}, libraw.Capabilities{}, err
		}
		file, err := libraw.Open(input, info.Size(), libraw.OpenOptions{})
		if err != nil {
			return libraw.Metadata{}, libraw.Capabilities{}, err
		}
		return file.Metadata(), file.Capabilities(), nil
	}
	_ = read
}

func ExampleFile_Preview() {
	extract := func(ctx context.Context, file *libraw.File, output io.Writer) error {
		preview, err := file.Preview(ctx)
		if err != nil {
			return err
		}
		_, err = io.Copy(output, bytes.NewReader(preview.JPEG))
		return err
	}
	_ = extract // Writes original embedded bytes, not a rendered RAW image.
}

func ExampleFile_DecodeRaw() {
	decode := func(ctx context.Context, file *libraw.File) (*libraw.RawImage, error) {
		return file.DecodeRaw(ctx)
	}
	_ = decode // Pixels are caller-owned unprocessed sensor codes.
}

func ExampleRender() {
	writePNG := func(ctx context.Context, raw *libraw.RawImage, output io.Writer) error {
		rgb, err := libraw.Render(ctx, raw, libraw.RenderOptions{})
		if err != nil {
			return err
		}
		return png.Encode(output, rgb)
	}
	_ = writePNG
}
