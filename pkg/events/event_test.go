package events

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/wrapperspb"
)

func sample(t *testing.T) Event {
	t.Helper()
	ev := Event{
		SpecVersion:     SpecVersion,
		ID:              "evt-01m5hqpzg00000000000000000",
		Source:          "qstack://compute/ru-central1",
		Type:            "qstack.compute.instance.updated",
		Subject:         "ins-01m5hqpzg00000000000000000",
		Time:            time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC),
		ProjectID:       "prj-01m5hqpzg00000000000000000",
		ResourceVersion: 42,
	}
	if err := ev.SetData(wrapperspb.String("payload")); err != nil {
		t.Fatal(err)
	}
	return ev
}

// REQ-PLT-090: the envelope is CloudEvents 1.0 JSON with the qStack
// extension attributes of spec 02 §8.
func TestEnvelopeFormat(t *testing.T) {
	ev := sample(t)
	b, err := ev.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]any{
		"specversion":       "1.0",
		"id":                ev.ID,
		"source":            "qstack://compute/ru-central1",
		"type":              "qstack.compute.instance.updated",
		"subject":           ev.Subject,
		"time":              "2026-10-21T10:00:00Z",
		"datacontenttype":   "application/json",
		"qsprojectid":       ev.ProjectID,
		"qsresourceversion": float64(42),
		"data":              "payload",
	} {
		if m[k] != want {
			t.Errorf("%s = %v, want %v", k, m[k], want)
		}
	}

	back, err := Unmarshal(b)
	if err != nil {
		t.Fatal(err)
	}
	got := &wrapperspb.StringValue{}
	if err := back.DecodeData(got); err != nil || got.GetValue() != "payload" || back.ResourceVersion != 42 {
		t.Errorf("round trip: %+v, %v", back, err)
	}
}

func TestEnvelopeValidation(t *testing.T) {
	ev := sample(t)
	ev.ID = ""
	if _, err := ev.Marshal(); err == nil {
		t.Error("Marshal without id succeeded")
	}
	for _, in := range []string{`{`, `{"specversion":"0.3","id":"x","type":"t"}`, `{"specversion":"1.0","type":"t"}`} {
		if _, err := Unmarshal([]byte(in)); err == nil {
			t.Errorf("Unmarshal(%s) succeeded", in)
		}
	}
	// New producer fields must not break old consumers.
	ev = sample(t)
	ev.Data = json.RawMessage(`"x"`)
	if err := ev.DecodeData(&wrapperspb.StringValue{}); err != nil {
		t.Error(err)
	}
}

// Spec 02 §8: subjects qs.<region>.<service>.<resource>.<change>, one stream
// per service; usage/audit/IAM streams are sources of their own.
func TestNaming(t *testing.T) {
	src := Source{Region: "ru-central1", Service: "compute"}
	if err := src.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := src.ResourceSubject("instance", Created); got != "qs.ru-central1.compute.instance.created" {
		t.Errorf("subject %s", got)
	}
	if got := src.ResourceType("instance", Deleted); got != "qstack.compute.instance.deleted" {
		t.Errorf("type %s", got)
	}
	if got := src.URI(); got != "qstack://compute/ru-central1" {
		t.Errorf("uri %s", got)
	}
	st := src.Stream()
	if st.Name != "qs-ru-central1-compute" || len(st.Subjects) != 1 || st.Subjects[0] != "qs.ru-central1.compute.>" {
		t.Errorf("stream %+v", st)
	}
	if got := (Source{Region: "global", Service: "iam"}).Stream().Subjects[0]; got != "qs.global.iam.>" {
		t.Errorf("iam stream %s", got)
	}
	for _, bad := range []Source{{"ru.central1", "compute"}, {"ru-central1", "comp*"}, {"", "compute"}, {"ru-central1", "Compute"}} {
		if bad.Validate() == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
	if ValidResourceName("a.b") || ValidResourceName(">") || !ValidResourceName("security_group") {
		t.Error("ValidResourceName")
	}
	if strings.Contains(src.Stream().Name, ".") {
		t.Error("stream names must not contain dots")
	}
}
