namespace Acme.Ledger;

/// <summary>One ledger entry for a settled payment.</summary>
public sealed record Entry(string PaymentId, long Cents);

/// <summary>Records entries in the general ledger table.</summary>
public sealed class Ledger
{
    private readonly List<Entry> entries = new();

    /// <summary>Adds an entry. A payment has one entry only.</summary>
    public bool Post(Entry entry)
    {
        if (entries.Exists(e => e.PaymentId == entry.PaymentId))
        {
            return false;
        }
        entries.Add(entry);
        return true;
    }
}
