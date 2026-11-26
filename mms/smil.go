package mms

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// maxSMIL: a presentation longer than this is not read (the parts are
// shown in the order they came).
const maxSMIL = 64 << 10

// Order lists a message's parts in the order its SMIL presentation names
// them (src="cid:..." by Content-ID, or by Content-Location), then the
// parts it does not name, in the order they came; the presentation
// itself is left out. A message without one keeps its order.
func Order(p *PDU) []Part {
	smil := -1
	for i, part := range p.Parts {
		if strings.EqualFold(part.ContentType, TypeSMIL) || (p.Start != "" && "<"+part.ContentID+">" == p.Start) {
			smil = i
			break
		}
	}
	var out []Part
	used := make([]bool, len(p.Parts))
	if smil >= 0 {
		used[smil] = true
		for _, src := range smilSources(p.Parts[smil].Data) {
			for i, part := range p.Parts {
				if used[i] {
					continue
				}
				id, isCID := strings.CutPrefix(src, "cid:")
				if (isCID && strings.Trim(id, "<>") == part.ContentID) || (!isCID && src != "" && (src == part.Location || src == part.ContentID || src == part.Name)) {
					used[i] = true
					out = append(out, part)
					break
				}
			}
		}
	}
	for i, part := range p.Parts {
		if !used[i] {
			out = append(out, part)
		}
	}
	return out
}

// smilSources is the src of every media element, in document order.
func smilSources(raw []byte) []string {
	if len(raw) > maxSMIL {
		return nil
	}
	d := xml.NewDecoder(bytes.NewReader(raw))
	d.Strict = false
	d.Entity = xml.HTMLEntity
	d.CharsetReader = func(_ string, in io.Reader) (io.Reader, error) { return in, nil }
	var out []string
	for n := 0; n < 4096; n++ {
		tok, err := d.Token()
		if err != nil {
			break
		}
		el, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch strings.ToLower(el.Name.Local) {
		case "img", "text", "audio", "video", "ref", "textstream", "animation":
			for _, a := range el.Attr {
				if strings.EqualFold(a.Name.Local, "src") {
					out = append(out, a.Value)
				}
			}
		}
	}
	return out
}

// SMIL is the presentation of an outgoing message: each media part (by its
// Content-Location) in a slide of its own region, text under images.
func SMIL(parts []Part) []byte {
	var b strings.Builder
	b.WriteString(`<smil><head><layout><root-layout width="320px" height="480px"/>`)
	b.WriteString(`<region id="Image" left="0" top="0" width="320px" height="320px" fit="meet"/>`)
	b.WriteString(`<region id="Text" left="0" top="320" width="320px" height="160px" fit="meet"/>`)
	b.WriteString(`</layout></head><body><par dur="5000ms">`)
	for _, p := range parts {
		loc := xmlEscape(p.Location)
		switch {
		case strings.HasPrefix(p.ContentType, "image/"):
			fmt.Fprintf(&b, `<img src="%s" region="Image"/>`, loc)
		case strings.HasPrefix(p.ContentType, "text/"):
			fmt.Fprintf(&b, `<text src="%s" region="Text"/>`, loc)
		case strings.HasPrefix(p.ContentType, "audio/"):
			fmt.Fprintf(&b, `<audio src="%s"/>`, loc)
		case strings.HasPrefix(p.ContentType, "video/"):
			fmt.Fprintf(&b, `<video src="%s" region="Image"/>`, loc)
		}
	}
	b.WriteString(`</par></body></smil>`)
	return []byte(b.String())
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
