// Package events defines qStack events (spec 02 §8): the CloudEvents 1.0
// envelope, NATS subjects and streams, a JetStream publisher and a consumer
// that processes every event exactly once against the consumer's database.
//
// Events are written by pkg/outbox in the transaction of the change and
// relayed to NATS at least once; JetStream drops duplicates within its
// window (Nats-Msg-Id = event ID) and Consumer drops the rest by event ID.
package events

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// SpecVersion is the CloudEvents version of the envelope.
const SpecVersion = "1.0"

// Event is a CloudEvents 1.0 envelope in JSON structured mode. Data is the
// protojson encoding of a message, e.g. the full resource.
type Event struct {
	SpecVersion     string          `json:"specversion"`
	ID              string          `json:"id"`
	Source          string          `json:"source"`
	Type            string          `json:"type"`
	Subject         string          `json:"subject,omitempty"`
	Time            time.Time       `json:"time"`
	DataContentType string          `json:"datacontenttype,omitempty"`
	ProjectID       string          `json:"qsprojectid,omitempty"`
	ResourceVersion int64           `json:"qsresourceversion,omitempty"`
	Data            json.RawMessage `json:"data,omitempty"`
}

// SetData stores m as protojson in Data.
func (e *Event) SetData(m proto.Message) error {
	b, err := protojson.Marshal(m)
	if err != nil {
		return fmt.Errorf("events: encode data: %w", err)
	}
	e.Data, e.DataContentType = b, "application/json"
	return nil
}

// DecodeData unmarshals Data into m. Unknown fields are ignored so that
// consumers keep working when the producer adds fields.
func (e *Event) DecodeData(m proto.Message) error {
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(e.Data, m); err != nil {
		return fmt.Errorf("events: decode data of %s: %w", e.ID, err)
	}
	return nil
}

// Marshal validates e and returns its JSON envelope.
func (e *Event) Marshal() ([]byte, error) {
	if e.SpecVersion != SpecVersion || e.ID == "" || e.Source == "" || e.Type == "" || e.Time.IsZero() {
		return nil, errors.New("events: specversion, id, source, type and time are required")
	}
	return json.Marshal(e)
}

// Unmarshal parses a JSON envelope.
func Unmarshal(b []byte) (Event, error) {
	var e Event
	if err := json.Unmarshal(b, &e); err != nil {
		return Event{}, fmt.Errorf("events: decode envelope: %w", err)
	}
	if e.SpecVersion != SpecVersion || e.ID == "" || e.Type == "" {
		return Event{}, errors.New("events: not a CloudEvents 1.0 envelope")
	}
	return e, nil
}

// Change is the kind of a resource change.
type Change string

// Resource changes; the last token of subjects and types.
const (
	Created Change = "created"
	Updated Change = "updated"
	Deleted Change = "deleted"
)

// token is one NATS subject token: no dots, wildcards or whitespace.
var token = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// Source is the producer: a service in a region ("global" for IAM).
type Source struct {
	Region  string // e.g. "ru-central1"
	Service string // e.g. "compute"
}

// Validate checks that region and service are valid subject tokens.
func (s Source) Validate() error {
	if !token.MatchString(s.Region) || !token.MatchString(s.Service) {
		return fmt.Errorf("events: invalid source %q/%q", s.Region, s.Service)
	}
	return nil
}

// URI is the CloudEvents source, e.g. "qstack://compute/ru-central1".
func (s Source) URI() string { return "qstack://" + s.Service + "/" + s.Region }

// ResourceSubject is the NATS subject of a resource change:
// qs.<region>.<service>.<resource>.<change>.
func (s Source) ResourceSubject(resource string, c Change) string {
	return "qs." + s.Region + "." + s.Service + "." + resource + "." + string(c)
}

// ResourceType is the CloudEvents type: qstack.<service>.<resource>.<change>.
func (s Source) ResourceType(resource string, c Change) string {
	return "qstack." + s.Service + "." + resource + "." + string(c)
}

// Stream returns the configuration of the service's own stream, which
// captures qs.<region>.<service>.> (spec 02 §8).
func (s Source) Stream() StreamConfig {
	return StreamConfig{
		Name:     "qs-" + s.Region + "-" + s.Service,
		Subjects: []string{"qs." + s.Region + "." + s.Service + ".>"},
	}
}

// ValidResourceName reports whether resource is usable as a subject token
// (e.g. "instance", "security_group").
func ValidResourceName(resource string) bool { return token.MatchString(resource) }
