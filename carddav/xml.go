package carddav

import (
	"bytes"
	"encoding/xml"
	"strconv"
	"strings"
)

const (
	nsDAV  = "DAV:"
	nsCard = "urn:ietf:params:xml:ns:carddav"
	nsCS   = "http://calendarserver.org/ns/"
)

// The request bodies. A client asks for little: ETags first, the cards'
// content only for what changed (multiget).
const (
	xmlHead = `<?xml version="1.0" encoding="utf-8"?>` + "\n"

	findPrincipal = xmlHead + `<D:propfind xmlns:D="DAV:" xmlns:C="` + nsCard + `"><D:prop>` +
		`<D:resourcetype/><D:current-user-principal/><C:addressbook-home-set/></D:prop></D:propfind>`

	findBooks = xmlHead + `<D:propfind xmlns:D="DAV:" xmlns:C="` + nsCard + `" xmlns:CS="` + nsCS + `"><D:prop>` +
		`<D:resourcetype/><D:displayname/><D:current-user-privilege-set/><D:supported-report-set/><D:sync-token/>` +
		`<CS:getctag/><C:supported-address-data/><C:max-resource-size/></D:prop></D:propfind>`

	listETags = xmlHead + `<D:propfind xmlns:D="DAV:"><D:prop><D:resourcetype/><D:getetag/></D:prop></D:propfind>`

	bookState = xmlHead + `<D:propfind xmlns:D="DAV:" xmlns:CS="` + nsCS + `"><D:prop><D:sync-token/><CS:getctag/></D:prop></D:propfind>`
)

func esc(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// syncBody is a sync-collection REPORT (RFC 6578 §3.2) for what changed
// since token ("" for everything), at most limit changes (0: no limit).
func syncBody(token string, limit int) []byte {
	var b strings.Builder
	b.WriteString(xmlHead + `<D:sync-collection xmlns:D="DAV:"><D:sync-token>` + esc(token) + `</D:sync-token><D:sync-level>1</D:sync-level>`)
	if limit > 0 {
		b.WriteString(`<D:limit><D:nresults>` + strconv.Itoa(limit) + `</D:nresults></D:limit>`)
	}
	b.WriteString(`<D:prop><D:getetag/></D:prop></D:sync-collection>`)
	return []byte(b.String())
}

// multigetBody is an addressbook-multiget REPORT (RFC 6352 §8.7).
func multigetBody(hrefs []string) []byte {
	var b strings.Builder
	b.WriteString(xmlHead + `<C:addressbook-multiget xmlns:D="DAV:" xmlns:C="` + nsCard + `"><D:prop><D:getetag/><C:address-data/></D:prop>`)
	for _, h := range hrefs {
		b.WriteString("<D:href>" + esc(h) + "</D:href>")
	}
	b.WriteString(`</C:addressbook-multiget>`)
	return []byte(b.String())
}

type multistatus struct {
	Responses []response `xml:"DAV: response"`
	SyncToken string     `xml:"DAV: sync-token"`
}

type response struct {
	Hrefs    []string   `xml:"DAV: href"`
	Status   string     `xml:"DAV: status"`
	Propstat []propstat `xml:"DAV: propstat"`
}

type propstat struct {
	Prop   prop   `xml:"DAV: prop"`
	Status string `xml:"DAV: status"`
}

type empty struct{}

type hrefProp struct {
	Href string `xml:"DAV: href"`
}

type prop struct {
	ResourceType *struct {
		Collection  *empty `xml:"DAV: collection"`
		AddressBook *empty `xml:"urn:ietf:params:xml:ns:carddav addressbook"`
	} `xml:"DAV: resourcetype"`
	DisplayName string    `xml:"DAV: displayname"`
	ETag        string    `xml:"DAV: getetag"`
	CTag        string    `xml:"http://calendarserver.org/ns/ getctag"`
	SyncToken   string    `xml:"DAV: sync-token"`
	Principal   *hrefProp `xml:"DAV: current-user-principal"`
	Home        *hrefProp `xml:"urn:ietf:params:xml:ns:carddav addressbook-home-set"`
	Reports     *struct {
		Supported []struct {
			Report struct {
				Any []struct {
					XMLName xml.Name
				} `xml:",any"`
			} `xml:"DAV: report"`
		} `xml:"DAV: supported-report"`
	} `xml:"DAV: supported-report-set"`
	Privileges *struct {
		Privilege []struct {
			Any []struct {
				XMLName xml.Name
			} `xml:",any"`
		} `xml:"DAV: privilege"`
	} `xml:"DAV: current-user-privilege-set"`
	AddressTypes *struct {
		Types []struct {
			ContentType string `xml:"content-type,attr"`
			Version     string `xml:"version,attr"`
		} `xml:"urn:ietf:params:xml:ns:carddav address-data-type"`
	} `xml:"urn:ietf:params:xml:ns:carddav supported-address-data"`
	MaxSize     string `xml:"urn:ietf:params:xml:ns:carddav max-resource-size"`
	AddressData string `xml:"urn:ietf:params:xml:ns:carddav address-data"`
}

// statusCode is the code of a status line ("HTTP/1.1 404 Not Found"), 0
// for none.
func statusCode(line string) int {
	f := strings.Fields(line)
	if len(f) < 2 {
		return 0
	}
	n, _ := strconv.Atoi(f[1])
	return n
}

// ok is the properties a response has (its 200 propstats), and its code:
// the response's own status, or 200 when it has found properties.
func (r *response) ok() (prop, int) {
	var out prop
	code := statusCode(r.Status)
	for _, ps := range r.Propstat {
		if c := statusCode(ps.Status); c < 200 || c > 299 {
			continue
		}
		p := ps.Prop
		if code == 0 {
			code = 200
		}
		if p.ResourceType != nil {
			out.ResourceType = p.ResourceType
		}
		for _, s := range []struct{ dst, src *string }{{&out.DisplayName, &p.DisplayName}, {&out.ETag, &p.ETag}, {&out.CTag, &p.CTag},
			{&out.SyncToken, &p.SyncToken}, {&out.MaxSize, &p.MaxSize}, {&out.AddressData, &p.AddressData}} {
			if *s.src != "" {
				*s.dst = *s.src
			}
		}
		if p.Principal != nil {
			out.Principal = p.Principal
		}
		if p.Home != nil {
			out.Home = p.Home
		}
		if p.Reports != nil {
			out.Reports = p.Reports
		}
		if p.Privileges != nil {
			out.Privileges = p.Privileges
		}
		if p.AddressTypes != nil {
			out.AddressTypes = p.AddressTypes
		}
	}
	return out, code
}

func (p prop) isBook() bool { return p.ResourceType != nil && p.ResourceType.AddressBook != nil }

func (p prop) isCollection() bool {
	return p.ResourceType != nil && (p.ResourceType.Collection != nil || p.ResourceType.AddressBook != nil)
}

func (p prop) reports(space, local string) bool {
	if p.Reports == nil {
		return false
	}
	for _, s := range p.Reports.Supported {
		for _, a := range s.Report.Any {
			if a.XMLName.Space == space && a.XMLName.Local == local {
				return true
			}
		}
	}
	return false
}

// writable: the privileges say the user may add cards (bind, write or all),
// or the server did not say.
func (p prop) writable() bool {
	if p.Privileges == nil {
		return true
	}
	for _, pr := range p.Privileges.Privilege {
		for _, a := range pr.Any {
			if a.XMLName.Space == nsDAV && (a.XMLName.Local == "write" || a.XMLName.Local == "bind" || a.XMLName.Local == "all") {
				return true
			}
		}
	}
	return false
}

// version is the vCard version to write to a book: 3.0 when it takes 3.0
// or does not say, 4.0 when that is all it takes.
func (p prop) version() string {
	if p.AddressTypes == nil || len(p.AddressTypes.Types) == 0 {
		return "3.0"
	}
	for _, t := range p.AddressTypes.Types {
		if t.Version == "3.0" || t.Version == "" {
			return "3.0"
		}
	}
	for _, t := range p.AddressTypes.Types {
		if t.Version == "4.0" {
			return "4.0"
		}
	}
	return "3.0"
}

func parseMultistatus(raw []byte) (*multistatus, error) {
	var ms multistatus
	if err := xml.NewDecoder(bytes.NewReader(raw)).Decode(&ms); err != nil {
		return nil, err
	}
	return &ms, nil
}
