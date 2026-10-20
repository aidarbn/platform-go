# Webhooks

`kit/webhookx` sends and receives webhooks in the [Standard Webhooks](https://www.standardwebhooks.com) format: the `webhook-id`, `webhook-timestamp` and `webhook-signature` headers, an HMAC-SHA256 of `id.timestamp.body`, secrets written as `whsec_` followed by base64. The other side signs or verifies with any Standard Webhooks library, there is nothing of the platform's own to explain to it. It needs the `postgres` module, and the `river` module for sending.

## Sending

A webhook is a River job inserted in the transaction of the change it reports, so it goes out exactly when the change commits — the guarantee an outbox table would give, with the queue already in the same database.

```go
sender, err := webhookx.NewSender(webhookx.SenderConfig{
	Endpoints: map[string]webhookx.Endpoint{
		"crm": {URL: crmURL, Secret: crmSecret}, // webhookx.ParseSecret("whsec_...")
	},
	Logger: app.Logger(),
})
if err != nil {
	return err
}
riverx.AddWorker(app, sender) // in every role: an api instance only inserts

// in a use case, inside its transaction
id, err := sender.Enqueue(ctx, riverx.QueueFrom(app), tx, "crm", "order.paid", order)
```

The body is `{"type": "order.paid", "timestamp": "...", "data": <order>}`, built once when the webhook is queued: every attempt repeats it with the same `webhook-id`, so the receiver drops copies, and a fresh timestamp and signature. The job carries the endpoint by name, never its URL or secret, so the job table holds no credentials and a rotated secret applies to pending retries.

- A 2xx answer completes the job, `410 Gone` cancels it, anything else is retried: `webhookx.DeliveryAttempts` (15) attempts with River's backoff, about two days.
- The last failed attempt logs the alert `webhook_delivery_failed` (`app.DeclareAlerts(webhookx.AlertDeliveryFailed)`); so does a pending job whose endpoint was removed from the configuration.
- Deliveries are counted in `river_jobs_total{kind="webhook_delivery"}` like every job.
- Endpoints come from the project's configuration, not from users: there is no protection against a URL pointing inside the network.

`webhookx.NewSecret()` makes a secret to hand to the receiving side.

## Receiving

```go
recv, err := webhookx.NewReceiver(webhookx.ReceiverConfig{
	Source:  "kaspi",
	Secrets: []webhookx.Secret{current, previous}, // previous only while rotating
	DB:      postgres.Pool(app),
	Handle: func(ctx context.Context, tx pgx.Tx, e webhookx.Event) error {
		return payments.Confirm(ctx, tx, e.Body)
	},
})
if err != nil {
	return err
}
api.HandleHTTP(app, "POST /webhooks/kaspi", recv)
```

The receiver verifies the signature against any of the secrets and the timestamp against its clock (5 minutes either way), then runs `Handle` inside a transaction that also records the `webhook-id` per source. Domain changes and jobs inserted with `tx` commit together with the record; an error rolls all of it back. A webhook is therefore processed once however many times it arrives, including copies arriving at the same moment.

| Answer | When |
|---|---|
| `204` | processed, or a copy of a processed webhook |
| `400` | headers missing or the timestamp unreadable |
| `401` | wrong signature or a timestamp outside the tolerance |
| `405`, `413` | not a POST, a body over `MaxBody` (1 MiB) |
| `500` | `Handle` failed: the sender retries |

Processed ids live in `platform_webhook_inbound`, versioned like the other platform tables, and are removed after `Retention` (24h), at most once an hour by the receiver itself. The retention must exceed twice the tolerance, or a replay could slip in after its id is gone; `NewReceiver` refuses a shorter one.

Senders that do not follow Standard Webhooks — most payment providers have their own scheme — are served with a plain `api.HandleHTTP` handler.
