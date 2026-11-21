package caldav

import (
	"bytes"
	"encoding/xml"
	"strconv"
	"strings"
)

const (
	nsDAV = "DAV:"
	nsCal = "urn:ietf:params:xml:ns:caldav"
	nsCS  = "http://calendarserver.org/ns/"
)

// The request bodies: ETags first, the objects' content only for what
// changed (calendar-multiget).
const (
	xmlHead = `<?xml version="1.0" encoding="utf-8"?>` + "\n"

	findPrincipal = xmlHead + `<D:propfind xmlns:D="DAV:" xmlns:C="` + nsCal + `"><D:prop>` +
		`<D:resourcetype/><D:current-user-principal/><C:calendar-home-set/></D:prop></D:propfind>`

	findCalendars = xmlHead + `<D:propfind xmlns:D="DAV:" xmlns:C="` + nsCal + `" xmlns:CS="` + nsCS + `"><D:prop>` +
		`<D:resourcetype/><D:displayname/><D:current-user-privilege-set/><D:supported-report-set/><D:sync-token/>` +
		`<CS:getctag/><C:supported-calendar-component-set/><C:max-resource-size/></D:prop></D:propfind>`
)

func esc(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// multigetBody is a calendar-multiget REPORT (RFC 4791 §7.9).
func multigetBody(hrefs []string) []byte {
	var b strings.Builder
	b.WriteString(xmlHead + `<C:calendar-multiget xmlns:D="DAV:" xmlns:C="` + nsCal + `"><D:prop><D:getetag/><C:calendar-data/></D:prop>`)
	for _, h := range hrefs {
		b.WriteString("<D:href>" + esc(h) + "</D:href>")
	}
	b.WriteString(`</C:calendar-multiget>`)
	return []byte(b.String())
}

type multistatus struct {
	Responses []response `xml:"DAV: response"`
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

type anyNames struct {
	Any []struct {
		XMLName xml.Name
	} `xml:",any"`
}

type prop struct {
	ResourceType *struct {
		Collection *empty `xml:"DAV: collection"`
		Calendar   *empty `xml:"urn:ietf:params:xml:ns:caldav calendar"`
	} `xml:"DAV: resourcetype"`
	DisplayName string    `xml:"DAV: displayname"`
	ETag        string    `xml:"DAV: getetag"`
	CTag        string    `xml:"http://calendarserver.org/ns/ getctag"`
	SyncToken   string    `xml:"DAV: sync-token"`
	Principal   *hrefProp `xml:"DAV: current-user-principal"`
	Home        *hrefProp `xml:"urn:ietf:params:xml:ns:caldav calendar-home-set"`
	Reports     *struct {
		Supported []struct {
			Report anyNames `xml:"DAV: report"`
		} `xml:"DAV: supported-report"`
	} `xml:"DAV: supported-report-set"`
	Privileges *struct {
		Privilege []anyNames `xml:"DAV: privilege"`
	} `xml:"DAV: current-user-privilege-set"`
	Components *struct {
		Comp []struct {
			Name string `xml:"name,attr"`
		} `xml:"urn:ietf:params:xml:ns:caldav comp"`
	} `xml:"urn:ietf:params:xml:ns:caldav supported-calendar-component-set"`
	MaxSize      string `xml:"urn:ietf:params:xml:ns:caldav max-resource-size"`
	CalendarData string `xml:"urn:ietf:params:xml:ns:caldav calendar-data"`
}

func statusCode(line string) int {
	f := strings.Fields(line)
	if len(f) < 2 {
		return 0
	}
	n, _ := strconv.Atoi(f[1])
	return n
}

// ok is a response's found properties (its 2xx propstats merged) and its
// code: its own status, or 200 when it has found properties.
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
			{&out.SyncToken, &p.SyncToken}, {&out.MaxSize, &p.MaxSize}, {&out.CalendarData, &p.CalendarData}} {
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
		if p.Components != nil {
			out.Components = p.Components
		}
	}
	return out, code
}

func (p prop) isCalendar() bool { return p.ResourceType != nil && p.ResourceType.Calendar != nil }

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

// writable: the privileges say the user may add objects, or the server
// did not say.
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

// events: the calendar takes VEVENTs (a calendar that does not say takes
// every component).
func (p prop) events() bool {
	if p.Components == nil || len(p.Components.Comp) == 0 {
		return true
	}
	for _, c := range p.Components.Comp {
		if strings.EqualFold(c.Name, "VEVENT") {
			return true
		}
	}
	return false
}

func parseMultistatus(raw []byte) (*multistatus, error) {
	var ms multistatus
	if err := xml.NewDecoder(bytes.NewReader(raw)).Decode(&ms); err != nil {
		return nil, err
	}
	return &ms, nil
}
