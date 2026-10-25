package api

import (
	"context"

	"google.golang.org/protobuf/types/known/timestamppb"

	rminternalv1 "github.com/mindevis/qstack/gen/go/qstack/resourcemanager/internalv1"
	rmv1 "github.com/mindevis/qstack/gen/go/qstack/resourcemanager/v1"
	"github.com/mindevis/qstack/internal/resourcemanager/app"
)

// GetQuotas returns the limited metrics of a project with their usage.
func (s *projects) GetQuotas(ctx context.Context, req *rmv1.GetQuotasRequest) (*rmv1.GetQuotasResponse, error) {
	p, err := s.loadProject(ctx, req.GetProjectId())
	if err != nil {
		return nil, err
	}
	qs, err := s.app.GetQuotas(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	resp := &rmv1.GetQuotasResponse{Quotas: make([]*rmv1.Quota, len(qs))}
	for i, q := range qs {
		resp.Quotas[i] = &rmv1.Quota{Metric: q.Metric, Limit: q.Limit, Used: q.Used, Reserved: q.Reserved}
	}
	return resp, nil
}

// quotas is the internal QuotaService behind pkg/quota (spec 03 §5).
type quotas struct {
	rminternalv1.UnimplementedQuotaServiceServer
	app *app.App
}

func (s *quotas) Reserve(ctx context.Context, req *rminternalv1.ReserveRequest) (*rminternalv1.ReserveResponse, error) {
	deltas := make(map[string]int64, len(req.GetDeltas()))
	for _, d := range req.GetDeltas() {
		deltas[d.GetMetric()] += d.GetAmount()
	}
	expire, err := s.app.Reserve(ctx, req.GetProjectId(), req.GetOperationId(), deltas)
	if err != nil {
		return nil, err
	}
	return &rminternalv1.ReserveResponse{ExpireTime: timestamppb.New(expire)}, nil
}

func (s *quotas) Commit(ctx context.Context, req *rminternalv1.CommitRequest) (*rminternalv1.CommitResponse, error) {
	return &rminternalv1.CommitResponse{}, s.app.Commit(ctx, req.GetOperationId())
}

func (s *quotas) Release(ctx context.Context, req *rminternalv1.ReleaseRequest) (*rminternalv1.ReleaseResponse, error) {
	return &rminternalv1.ReleaseResponse{}, s.app.Release(ctx, req.GetOperationId())
}
