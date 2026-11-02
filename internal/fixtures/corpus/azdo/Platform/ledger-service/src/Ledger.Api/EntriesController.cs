using Acme.Ledger;
using Microsoft.AspNetCore.Mvc;

namespace Acme.Ledger.Api;

/// <summary>Serves POST /v1/entries for the Payments API.</summary>
[ApiController]
[Route("v1/entries")]
public sealed class EntriesController : ControllerBase
{
    private readonly Ledger ledger;

    public EntriesController(Ledger ledger) => this.ledger = ledger;

    [HttpPost]
    public IActionResult Post(Entry entry) => ledger.Post(entry) ? NoContent() : Conflict();
}
