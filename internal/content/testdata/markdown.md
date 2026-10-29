# Reminder service

The reminder service sends **reminder emails** to reviewers. It runs in the
`acme-prod` cluster.

## Owners

- Team Hermes owns the service.
- On-call rota: see [the rota](https://wiki.acme.test/rota).
  - Nested item with _emphasis_.

## Interfaces

| Endpoint | Method | Purpose |
|---|---|---|
| /reminders | POST | Schedule a reminder |
| /reminders/{id} | DELETE | Cancel a reminder |

```go
func Send(r Reminder) error {
	return mailer.Send(r)
}
```

> The service must not send more than one reminder per day.

---

<div>Raw <b>HTML</b> block</div>

# Operations

Deploy with `make deploy`.
