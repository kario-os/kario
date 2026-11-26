package mms

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"strings"
)

// Attachment is a picture (or other media) going into an outgoing message.
type Attachment struct {
	ContentType string
	Name        string
	Data        []byte
}

// NewSendReq is an m-send.req to the recipients: the presentation, then
// each attachment, then the text (UTF-8), in a multipart/related body
// whose start is the presentation. A delivery report is asked for.
func NewSendReq(txid string, to []string, subject, text string, atts []Attachment) *PDU {
	var media []Part
	for i, a := range atts {
		name := a.Name
		if name == "" {
			name = fmt.Sprintf("image%d%s", i+1, extOf(a.ContentType))
		}
		media = append(media, Part{ContentType: a.ContentType, Name: name, ContentID: name, Location: name, Data: a.Data})
	}
	if strings.TrimSpace(text) != "" {
		media = append(media, Part{ContentType: TypeText, Charset: "utf-8", Name: "text.txt", ContentID: "text.txt", Location: "text.txt", Data: []byte(text)})
	}
	parts := append([]Part{{ContentType: TypeSMIL, Name: "smil.xml", ContentID: "smil", Location: "smil.xml", Data: SMIL(media)}}, media...)
	return &PDU{Type: SendReq, TransactionID: txid, To: to, Subject: subject, Class: "personal", DeliveryReport: true,
		ContentType: TypeRelated, Start: "<smil>", Related: TypeSMIL, Parts: parts}
}

func extOf(mediaType string) string {
	switch strings.ToLower(mediaType) {
	case TypeJPEG:
		return ".jpg"
	case TypePNG:
		return ".png"
	case TypeGIF:
		return ".gif"
	}
	return ""
}

// Limits of a picture going out: the side an MMS picture is scaled to
// (phones show nothing larger), and the largest picture read at all.
const (
	maxSendSide = 1280
	maxReadSide = 8192
)

// FitImage makes a PNG, JPEG or GIF picture fit an outgoing message: one
// small enough already goes as it is; a larger one is scaled down and
// written as JPEG, smaller and plainer until it is at most limit bytes.
func FitImage(raw []byte, limit int) ([]byte, string, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return nil, "", fmt.Errorf("mms: not a picture: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > maxReadSide || cfg.Height > maxReadSide {
		return nil, "", errors.New("mms: picture too large")
	}
	mediaType := map[string]string{"jpeg": TypeJPEG, "png": TypePNG, "gif": TypeGIF}[format]
	if mediaType == "" {
		return nil, "", errors.New("mms: not a PNG, JPEG or GIF picture")
	}
	if len(raw) <= limit && max(cfg.Width, cfg.Height) <= maxSendSide {
		return raw, mediaType, nil
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, "", err
	}
	for _, side := range []int{maxSendSide, 960, 640, 480, 320} {
		small := shrink(img, side)
		for _, q := range []int{85, 70, 55} {
			var b bytes.Buffer
			if err := jpeg.Encode(&b, small, &jpeg.Options{Quality: q}); err != nil {
				return nil, "", err
			}
			if b.Len() <= limit {
				return b.Bytes(), TypeJPEG, nil
			}
		}
	}
	return nil, "", errors.New("mms: the picture does not fit in a message")
}

// shrink scales an image so its longer side is at most side, averaging the
// source pixels each one covers.
func shrink(img image.Image, side int) image.Image {
	bnd := img.Bounds()
	w, h := bnd.Dx(), bnd.Dy()
	if max(w, h) <= side {
		return img
	}
	nw, nh := w*side/max(w, h), h*side/max(w, h)
	nw, nh = max(nw, 1), max(nh, 1)
	out := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < nh; y++ {
		y0, y1 := y*h/nh, max((y+1)*h/nh, y*h/nh+1)
		for x := 0; x < nw; x++ {
			x0, x1 := x*w/nw, max((x+1)*w/nw, x*w/nw+1)
			var r, g, b, n uint64
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					cr, cg, cb, _ := img.At(bnd.Min.X+sx, bnd.Min.Y+sy).RGBA()
					r, g, b, n = r+uint64(cr), g+uint64(cg), b+uint64(cb), n+1
				}
			}
			out.SetRGBA(x, y, color.RGBA{R: uint8(r / n >> 8), G: uint8(g / n >> 8), B: uint8(b / n >> 8), A: 0xff})
		}
	}
	return out
}
