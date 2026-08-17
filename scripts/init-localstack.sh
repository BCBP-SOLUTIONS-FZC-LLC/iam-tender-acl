#!/usr/bin/env bash
# LocalStack ready-hook — provisions this service's two SQS subscriptions,
# each with its own DLQ (maxReceiveCount=5):
#   - tenant-lifecycle-tenderacl-q   — TenantOffboarded (LLD §10.1)
#   - member-removal-tenderacl-q     — TenantMembershipRemoved (ADR-0007
#     Wave 3 Phase 3, O_AND_M_DELTA.md §5 Option B)
# Matches api/asyncapi.yaml. Runs automatically on container start via the
# volume mount to /etc/localstack/init/ready.d/.
#
# This service is receive-only: it never publishes anything (TAC-EVT-1,
# TAC-D5), so unlike sibling IAM services there is no SNS topic to create
# and no SNS→SQS subscription to wire — in production these queues are
# populated by SendMessage calls from iam-org-membership (the existing
# TenantOffboarded relay, and the new TenantMembershipRemoved emit inside
# MembershipService.RemoveUser). For local dev/testing, send a message
# directly, e.g.:
#   awslocal sqs send-message --queue-url <tenant-lifecycle queue url> \
#     --message-body '{"id":"...","type":"TenantOffboarded","tenant_id":"...","time":"..."}'
#   awslocal sqs send-message --queue-url <member-removal queue url> \
#     --message-body '{"id":"...","type":"TenantMembershipRemoved","tenant_id":"...","subject":"<user_id>","time":"..."}'

set -euo pipefail

AWS_ACCOUNT=000000000000
AWS_REGION=us-east-1
MAX_RECEIVES=5

queue_arn() { printf 'arn:aws:sqs:%s:%s:%s' "$AWS_REGION" "$AWS_ACCOUNT" "$1"; }

provision_queue() {
	local queue="$1"
	local dlq="${queue}-dlq"

	awslocal sqs create-queue --queue-name "$dlq" >/dev/null

	local attrs
	attrs=$(mktemp)
	cat >"$attrs" <<EOF
{
  "RedrivePolicy": "{\"deadLetterTargetArn\":\"$(queue_arn "$dlq")\",\"maxReceiveCount\":\"${MAX_RECEIVES}\"}"
}
EOF
	awslocal sqs create-queue --queue-name "$queue" --attributes "file://$attrs" >/dev/null
	rm -f "$attrs"

	echo "  Queue: $(awslocal sqs get-queue-url --queue-name "$queue" --output text)"
	echo "  DLQ:   $(awslocal sqs get-queue-url --queue-name "$dlq" --output text)"
}

provision_queue "tenant-lifecycle-tenderacl-q"
provision_queue "member-removal-tenderacl-q"

echo "LocalStack init complete."
