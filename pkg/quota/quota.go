// Package quota is the client of project quotas (spec 02 §12, 03 §5):
// a mutating method reserves the resources it is about to create for its
// operation, and the controller commits the reservation when the resource
// exists or releases it when the operation fails.
//
//	quota.Reserve(ctx, projectID, op.Id, quota.Delta{"compute.instances.count", 1}, …)
//	… later, in the controller …
//	quota.Commit(ctx, op.Id)   // or quota.Release(ctx, op.Id)
//
// Amounts are integer units (count, bytes, vCPUs); never floats. Deleting a
// resource reserves and commits negative deltas, which never fail.
// Unconfirmed reservations expire after an hour.
package quota

import (
	"context"
	"time"

	"google.golang.org/grpc"

	rminternalv1 "github.com/mindevis/qstack/gen/go/qstack/resourcemanager/internalv1"
)

// ReservationTTL is how long an unconfirmed reservation holds quota.
const ReservationTTL = time.Hour

// Delta changes one metric of a project.
type Delta struct {
	Metric string // e.g. "compute.vcpus.total"
	Amount int64
}

// Quotas reserves, commits and releases project quota. Errors are API
// errors: RESOURCE_EXHAUSTED / QS_QUOTA_EXCEEDED with QuotaFailure details
// when a limit would be exceeded; return them to the caller unchanged.
type Quotas interface {
	// Reserve holds deltas for operationID; repeating it with the same
	// deltas is a no-op, with different deltas INVALID_ARGUMENT.
	Reserve(ctx context.Context, projectID, operationID string, deltas ...Delta) error
	// Commit turns the reservation into usage; repeating it is a no-op.
	Commit(ctx context.Context, operationID string) error
	// Release drops the reservation; repeating it is a no-op.
	Release(ctx context.Context, operationID string) error
}

// Client implements Quotas over qstack.resourcemanager.internal.v1.QuotaService.
type Client struct {
	svc rminternalv1.QuotaServiceClient
}

// NewClient returns a Client over conn.
func NewClient(conn grpc.ClientConnInterface) *Client {
	return &Client{svc: rminternalv1.NewQuotaServiceClient(conn)}
}

// Reserve implements Quotas.
func (c *Client) Reserve(ctx context.Context, projectID, operationID string, deltas ...Delta) error {
	req := &rminternalv1.ReserveRequest{ProjectId: projectID, OperationId: operationID}
	for _, d := range deltas {
		req.Deltas = append(req.Deltas, &rminternalv1.QuotaDelta{Metric: d.Metric, Amount: d.Amount})
	}
	_, err := c.svc.Reserve(ctx, req)
	return err
}

// Commit implements Quotas.
func (c *Client) Commit(ctx context.Context, operationID string) error {
	_, err := c.svc.Commit(ctx, &rminternalv1.CommitRequest{OperationId: operationID})
	return err
}

// Release implements Quotas.
func (c *Client) Release(ctx context.Context, operationID string) error {
	_, err := c.svc.Release(ctx, &rminternalv1.ReleaseRequest{OperationId: operationID})
	return err
}
