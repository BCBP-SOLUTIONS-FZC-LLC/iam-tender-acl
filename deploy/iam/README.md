# AWS IAM policy for iam-tender-acl

This directory holds the reference IAM policy that must be attached to the
service's IRSA role. The application does not create the role itself — that
is managed by platform Terraform. Copy the JSON in `policy.json` into the
Terraform module or apply it directly via `aws iam create-policy` /
`aws iam put-role-policy`.

The role is assumed by the service's Kubernetes ServiceAccount via IRSA. Wire
the ARN into Helm via `serviceAccount.annotations`:

```yaml
serviceAccount:
  create: true
  annotations:
    eks.amazonaws.com/role-arn: arn:aws:iam::ACCOUNT_ID:role/iam-tender-acl
```

## What iam-tender-acl needs (and does NOT need)

This service is the system of record for `tender_acl_entries` and a
receive-only consumer of `TenantMembershipsPurged` and `MembershipRevoked`.
It does **not** publish any
event (there is no outbox, no SNS producer, and no Glue schema
registration anywhere in this codebase, per TAC-EVT-1/TAC-D5), does
**not** store binary blobs, does **not** encrypt anything with a
service-owned KMS key (RDS handles at-rest encryption transparently), and
does **not** touch S3. The one synchronous outbound dependency this
service has — the grant-time membership check against
`iam-org-membership` (LLD §7.6.2) — is a plain mesh-internal HTTP call,
not an AWS API call, so it needs **no IAM grant at all**; it's governed by
the mesh/NetworkPolicy (see `deploy/helm/tender-acl/templates/networkpolicy.yaml`),
not IAM. If you find yourself adding `sns:Publish`, `s3:*`, `glue:*`, or
`kms:*` to this policy, stop — none of those belong to this service.

## Grants breakdown

| Sid | AWS service | Actions | Purpose |
|---|---|---|---|
| `ConsumeTenantLifecycleQueue` | SQS | `ReceiveMessage`, `DeleteMessage`, `GetQueueAttributes`, `ChangeMessageVisibility` | Two SQS consumers: `tenant-lifecycle-tenderacl-q` (tenant offboarding cascade-delete) and `member-removal-tenderacl-q` (ADR-0007 Wave 3 Phase 3 — per-user-removal ACL cascade). No `SendMessage` — the pod never publishes anywhere, including to either DLQ; redrive after `maxReceiveCount=5` is handled entirely by each queue's own redrive policy. |
| `CloudWatchLogs` | Logs | `CreateLogStream`, `PutLogEvents` | Container stdout when the OTel collector is not in the log pipeline. |

## Explicitly out of scope

- **No SNS.** This service publishes zero events. It consumes
  `TenantMembershipsPurged` and `MembershipRevoked` and nothing else.
- **No Glue Schema Registry.** There is no schema governance step here —
  `api/asyncapi.yaml` documents the two consumed events by hand (LLD §10.4).
- **No S3.** Never read, written, or listed.
- **No KMS.** RDS-at-rest encryption uses an AWS-managed key; the pod does
  not participate. No client-side envelope encryption is performed.
- **No STS AssumeRole beyond IRSA.** The pod does not chain-assume any
  cross-account roles.
- **No DLQ access.** The pod role has no SQS permissions on either
  `tenant-lifecycle-tenderacl-q-dlq` or `member-removal-tenderacl-q-dlq`.
  Manual redrive/inspection is an operator action performed with a
  separate, more privileged role.

## Rolling out

Order of operations for production:

1. Apply the new policy in Terraform / IAM console.
2. Verify the grant from within a debug pod that shares the ServiceAccount:
   ```bash
   aws sqs get-queue-attributes --queue-url <tenant_lifecycle_tenderacl_queue_url>  # expect Attributes
   ```
3. Deploy the new image + Helm chart.
4. Watch `iam_cascade_operations_total{service="tender-acl", event_type="TenantMembershipsPurged", result="error"}` — a
   rising rate immediately after rollout means the policy did not apply
   (SQS access denied). Roll back if so.
