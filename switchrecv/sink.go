package switchrecv

// The kinds of data the protocol moves: hello's accept, the item paths
// (/v1/items/<kind>) and the done reply name them so. A file's own kind is
// the singular FilePhoto or FileVideo.
const (
	KindContacts = "contacts"
	KindCallLog  = "call_log" // const-check: ok (the protocol's wire name; ui.SwitchCallLog is the same kind for the apps)
	KindSMS      = "sms"
	KindCalendar = "calendar"
	KindPhotos   = "photos"
	KindVideos   = "videos"

	FilePhoto = "photo"
	FileVideo = "video"
)

// Kinds are all of them, in the order the replies list them.
var Kinds = []string{KindContacts, KindCallLog, KindSMS, KindCalendar, KindPhotos, KindVideos}

// itemKinds come in batches of items; the others as files.
var itemKinds = map[string]bool{KindContacts: true, KindCallLog: true, KindSMS: true, KindCalendar: true}

// CallEntry is a call-log item. Type: in (answered), out, missed or
// rejected; At in Unix seconds, Duration in seconds.
type CallEntry struct {
	Number   string
	Name     string
	Type     string
	At       int64
	Duration int64
}

// Message is an SMS item: Type in or out, At in Unix milliseconds.
type Message struct {
	Address string
	Body    string
	Type    string
	At      int64
	Read    bool
}

// File is a photo or video received whole, its SHA-256 checked. Path is the
// staged copy: the sink moves it where it keeps files; whatever is left
// there after File returns is deleted.
type File struct {
	Path    string
	ID      string
	Kind    string // FilePhoto or FileVideo
	Name    string
	Mime    string // "" when the app gave none that reads as a media type
	Size    int64
	TakenAt int64 // Unix seconds, 0 unknown
}

// Outcome is what the sink did with an item.
type Outcome uint8

const (
	Stored    Outcome = iota
	Duplicate         // the destination had it already
	Rejected          // unreadable, or the destination is full
)

// Result is one item's outcome; Reason (for Rejected) is a short English
// phrase without the item's content.
type Result struct {
	Outcome Outcome
	Reason  string
}

// Sink takes what arrives, a batch at a time and in the order sent; it is
// called from the HTTP handler's goroutine, one call at a time. A batch
// method gives a Result per item; an error means nothing of the batch was
// kept and the app should send it again later (503).
type Sink interface {
	Contacts(vcards []string) ([]Result, error)
	CallLog(entries []CallEntry) ([]Result, error)
	SMS(msgs []Message) ([]Result, error)
	// Calendar gets VEVENTs, each from BEGIN:VEVENT to END:VEVENT with
	// CRLF line ends and no line end after the last.
	Calendar(events []string) ([]Result, error)
	File(f File) error
}
