# ADR-0039: Billing is a run, not a flag

Date: 2026-11-27

Status: Accepted

## Context

The money layer was complete and unanswerable. An entry carried a frozen
billing snapshot from the moment it was priced ([ADR-0014](0014-exact-money-and-duration.md)),
reports totalled it, exports itemised it, budgets consumed it — and nothing
anywhere recorded that an invoice had been sent.

The consequences were not subtle once looked for:

* The application could not answer "what is still unbilled", which is the
  question the whole money layer exists to serve.
* Nothing stopped the same fortnight going out twice. The only defence was
  somebody remembering which ranges they had already exported.
* An hour that had been invoiced stayed freely editable. Correcting it made the
  invoice wrong, silently, with nothing on any screen to say so.

Weekly submit-and-approve ([ADR-0023](0023-week-as-the-unit-of-approval.md))
covers the *internal* end of a timesheet's life. Nothing covered the external
end.

## Decision

Record billing as a **run**: a row per customer per period capturing what it
included, with the entries pointing back at it.

```
billing_runs(id, customer_id, starts_on, ends_on, reference, note,
             currency, entry_count, billable_seconds, amount_minor,
             created_by, created_at, voided_at, voided_by)
time_entries.billing_run_id   -- 0 means unbilled
```

Five decisions make it up.

**A run, not a flag on each entry.** A flag would say "this was billed" and
nothing else: no record of what any one invoice contained, so correcting one
would mean finding its entries by hand. With a run, *unbilled* is "belongs to no
run" — a query rather than a convention — and an invoice has a shadow here that
can be looked at.

**The totals are copied, not derived.** The run stores its own hours and amount
rather than summing its entries on demand, for exactly the reason an entry
stores its own snapshot: an invoice is a statement about a moment, and a rate
that changes next quarter must not change what last quarter's invoice said.

**Billed time is frozen.** Editing, deleting or moving an entry that belongs to
a run is refused, with a message naming the run and the way back. This is the
week lock one step further out, and sharper: a changed approved week is a
timesheet somebody re-approves, while a changed billed hour is an invoice that
no longer matches what was sent.

**Voiding, not deleting.** An invoice is sometimes credited, and the fact that
it existed is part of the account's history. A voided run stays on the list,
marked, and releases its entries — which become unbilled and go on the next run.

**Billing is the customer's administrator's.** `ActionManage` on the customer:
an administrator, or a manager of one of its projects. That is the same
authority that sets the customer's rates, which is the right shape, since
billing is the act those rates exist for. A member cannot, and neither can a
client — a client's own billing run id is removed by the projection, because
which invoice an hour went out on is the supplier's bookkeeping.

## Consequences

Good:

* "What is still unbilled" is a query, and the screen that answers it shows the
  entries behind the figure rather than only the figure.
* Billing the same period twice is refused by the data, not by memory. The
  stamping statement repeats the condition that chose the entries, so a run
  prepared while another was being written fails rather than double-bills.
* A run is a place to put the invoice number from whatever system issues
  invoices, which is the join between this application and that one.

Accepted costs:

* **Expenses are not covered.** A run holds time. An expense is billable and is
  not stamped, so "unbilled" is true of time only. This is stated rather than
  implied; extending the run to expenses is additive and needs no change to the
  shape above.
* **This is not an invoice and does not number one.** Invoicing is out of scope
  (DESIGN.md §11). A run that is recorded without a reference is a note to
  ourselves that time went out, which is less useful than one with the number
  in it, and the form says so rather than enforcing it.
* **A mixed-currency range is refused rather than split.** Somebody billing a
  customer who is invoiced in two currencies has to narrow the range. Summing
  them would produce a figure that is wrong in a way nobody notices until it is
  queried.
* **Freezing billed time will annoy somebody.** A typo noticed after the
  invoice went out now needs the run voided and remade. That is the correct
  amount of friction for changing what a customer was charged, and the refusal
  names the remedy rather than leaving a dead end.
