package frontend

import (
	"errors"
	"image"
	"image/draw"
)

// Only the SDL thread owns/reuses dst. Received images remain immutable, and no
// pixel pointer is retained by SDL_UpdateTexture after the synchronous call.
func prepareRGBA(src image.Image, dst *image.RGBA) (*image.RGBA, error) {
	size := src.Bounds().Size()
	if size.X < 1 || size.Y < 1 || size.X > 3840 || size.Y > 2160 || int64(size.X)*int64(size.Y) > 3840*2160 {
		return nil, errors.New("RENDER_UPLOAD_FAILED")
	}
	if rgba, ok := src.(*image.RGBA); ok && rgba.Bounds().Min == (image.Point{}) {
		return rgba, nil
	}
	if dst == nil || dst.Bounds().Size() != size {
		dst = image.NewRGBA(image.Rect(0, 0, size.X, size.Y))
	}
	draw.Draw(dst, dst.Bounds(), src, src.Bounds().Min, draw.Src)
	return dst, nil
}
