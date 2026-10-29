# Architecture

The Payments API is a Go service. It stores payments in PostgreSQL.

## Dependencies

- The ledger service records each settled payment.
- The reminder service sends payment reminder emails.
