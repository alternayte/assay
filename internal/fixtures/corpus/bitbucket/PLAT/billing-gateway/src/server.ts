// The billing gateway sends invoice files to the bank.
export function sendInvoiceFile(name: string): string {
  return `sent ${name}`;
}
