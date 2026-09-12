package port

import "context"

// TxRunner opens a database transaction and injects the pgx.Tx into ctx
// so repositories and the processed_events ledger can join it (withPool).
// Matches iam-org-membership / iam-realm-provisioner — without an outbox
// publisher: this service publishes zero events (TAC-EVT-1).
type TxRunner interface {
	RunInTx(ctx context.Context, fn func(ctx context.Context) error) error
}
