import { capture } from "./api";

/** One article fee in the cart. */
export interface Fee {
  article: string;
  cents: number;
}

/** Sums the fees and captures the payment. */
export async function checkout(paymentId: string, fees: Fee[]): Promise<number> {
  const total = fees.reduce((sum, f) => sum + f.cents, 0);
  await capture(paymentId);
  return total;
}
