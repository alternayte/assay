---
title: Ledger
---

# Ledger

The ledger records each settled payment exactly once. The ledger service reads settled payments from the Payments API and writes one entry for each payment.

## Reconciliation

Finance reconciles the ledger with the bank statements each night. See the [Payments API reference](/web/reference/payments-api) for the payment states.

![Ledger flow](/web/files/ledger-flow.png)
