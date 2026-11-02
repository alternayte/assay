/** The Payments API that takes the card payments. */
export const PAYMENTS = "https://payments.acme.test/v1/payments";

/** Captures an authorized payment. */
export async function capture(id: string): Promise<void> {
  const res = await fetch(`${PAYMENTS}/${id}/capture`, { method: "POST" });
  if (!res.ok) {
    throw new Error(`capture failed with ${res.status}`);
  }
}
