package quota

import (
	"context"
	"maps"
	"slices"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	rminternalv1 "github.com/mindevis/qstack/gen/go/qstack/resourcemanager/internalv1"
	"github.com/mindevis/qstack/pkg/events"
	"github.com/mindevis/qstack/pkg/outbox"
)

// SnapshotInterval is how often services publish their usage (REQ-RM-002).
const SnapshotInterval = time.Hour

// SnapshotSubject is the subject of a service's usage snapshot in region:
// qs.<region>.usage.snapshot.<service>, in the usage stream of the region.
func SnapshotSubject(region, service string) string {
	return "qs." + region + ".usage.snapshot." + service
}

// SnapshotType is the CloudEvents type of a service's usage snapshot.
func SnapshotType(service string) string { return "qstack." + service + ".usage.snapshot" }

// UsageStream is the region's usage stream qs-<region>-usage
// (qs.<region>.usage.>, spec 02 §8); producers and consumers ensure it.
func UsageStream(region string) events.StreamConfig {
	return events.StreamConfig{Name: "qs-" + region + "-usage", Subjects: []string{"qs." + region + ".usage.>"}}
}

// PublishSnapshot writes the hourly usage snapshot of a service to the
// outbox in tx: usage is project → metric → actual amount, counted at t.
// Projects without resources may be left out.
func PublishSnapshot(ctx context.Context, tx outbox.DB, w *outbox.Writer, region, service string, t time.Time, usage map[string]map[string]int64) error {
	snap := &rminternalv1.UsageSnapshot{Service: service, SnapshotTime: timestamppb.New(t)}
	for _, p := range slices.Sorted(maps.Keys(usage)) {
		pu := &rminternalv1.ProjectUsage{ProjectId: p}
		for _, m := range slices.Sorted(maps.Keys(usage[p])) {
			pu.Usage = append(pu.Usage, &rminternalv1.QuotaDelta{Metric: m, Amount: usage[p][m]})
		}
		snap.Projects = append(snap.Projects, pu)
	}
	ev := events.Event{Type: SnapshotType(service), Subject: service, Time: t}
	if err := ev.SetData(snap); err != nil {
		return err
	}
	_, err := w.Add(ctx, tx, SnapshotSubject(region, service), ev)
	return err
}
