package carddav

import "testing"

// FuzzMultistatus is a server's answer to a PROPFIND or a REPORT: it is
// read or refused, never more.
func FuzzMultistatus(f *testing.F) {
	f.Add([]byte(`<?xml version="1.0"?><D:multistatus xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav"><D:response><D:href>/b/</D:href>` +
		`<D:propstat><D:prop><D:resourcetype><D:collection/><C:addressbook/></D:resourcetype><D:supported-report-set><D:supported-report><D:report><D:sync-collection/></D:report></D:supported-report></D:supported-report-set>` +
		`<C:supported-address-data><C:address-data-type content-type="text/vcard" version="4.0"/></C:supported-address-data></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>` +
		`<D:response><D:href>/b/1.vcf</D:href><D:status>HTTP/1.1 404 Not Found</D:status></D:response><D:sync-token>t</D:sync-token></D:multistatus>`))
	f.Add([]byte(`<multistatus xmlns="DAV:"><response><href>x</href><propstat><prop><getetag>W/"1"</getetag></prop><status>HTTP/1.1 200</status></propstat></response></multistatus>`))
	f.Fuzz(func(t *testing.T, data []byte) {
		ms, err := parseMultistatus(data)
		if err != nil {
			return
		}
		for i := range ms.Responses {
			p, _ := ms.Responses[i].ok()
			_, _, _, _ = p.isBook(), p.writable(), p.version(), p.reports(nsDAV, "sync-collection")
			_ = normETag(p.ETag)
		}
	})
}
