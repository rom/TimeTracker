# ADR-0024: Overtime and travel are a kind on the entry, priced by customer rules

* **Status:** accepted
* **Date:** 2026-08-18
* **Addresses:** ASR-021

## Context

The base hourly rate answers "what is an hour worth". Real consulting contracts
also settle three questions it cannot:

* what an hour is worth when it is the tenth one that day;
* what an hour is worth when it is spent on a train rather than on the work;
* what gets paid back, at what markup, and against what evidence.

Every customer answers those differently, and their answers are contract terms
rather than preferences — which is why they belong on the customer and not in a
global setting, where they would be wrong for everybody.

The hard question is not where to store them. It is **who decides that a
particular hour is overtime.**

The obvious implementation reads a threshold — "over eight hours a day" — and
reclassifies the excess automatically. It is wrong, and expensively so. Whether
an hour counts as overtime is a contractual judgement: it may need authorisation
in advance, it may not apply to salaried staff, it may have been agreed for one
week only. A tool that bills hour nine at one and a half times the rate because
somebody forgot to stop a timer has not saved anyone work; it has manufactured
an invoice dispute, and the person who has to explain it is the one who did not
make the decision.

There is a second problem with deriving it. Amounts are frozen onto the entry
when it is recorded ([ADR-0014](0014-exact-money-and-duration.md)). Derived
overtime would make an entry's value depend on the other entries around it, so
editing Monday would silently change what Thursday was worth — and the frozen
figure would already have gone out on an invoice.

## Decision

**The kind of time is a property of the entry, chosen by the person.** An entry
is `work`, `overtime` or `travel`. Empty means work, so every entry recorded
before this existed keeps its meaning and no migration has to invent history.

**The customer's rules say what each kind is worth**, and nothing else:

| Rule | Effect |
|---|---|
| overtime rate | absolute hourly rate for overtime |
| overtime multiplier | percent of the resolved base rate; 150 is time and a half |
| travel billing | as work (default), at its own rate, or recorded but never invoiced |
| travel rate / multiplier | as above, for travel |
| expense markup | default markup on the billable side of a claim |
| mileage rate, per diem | price a quantity, so a distance can be claimed directly |
| receipt threshold | above this, a claim needs evidence attached |

An absolute rate beats a multiplier, because a contract naming a figure is
naming it *instead of* a multiple, not as well as one. An unset rule inherits:
overtime with no terms agreed bills at the ordinary rate, because inventing a
multiplier nobody agreed to would put an unsupported number on an invoice.

**The thresholds drive a prompt, and only a prompt.** A day or week past the
customer's threshold with nothing marked as overtime produces a notice on the
week view — the screen where hours are reviewed before submission — in exactly
the spirit of the existing gap detection: the tool reports what it observed and
leaves the judgement with the person who did the work. Marking the excess makes
the notice stop.

**"We do not pay for travel" is its own state, not a zero rate.** The time is
recorded in full and carries no amount. A 0% multiplier would say something
different to whoever reads the timesheet later, and zero is already how these
columns spell "not set".

**The kind reaches every export.** A line billed at one and a half times the
base rate has to say why on the document carrying the figure. An invoice number
that cannot be explained from the document it appears on is a dispute waiting to
happen.

**A quantity-priced expense stores its quantity, its unit and the rate used.**
42.5 km at 25.00/km is stored as thousandths of a unit, an integer, for the same
reason every other quantity here is an integer — and the rate is recorded on the
claim so the amount can be checked rather than trusted.

**The receipt threshold is enforced at submission.** It cannot be enforced when
an expense is created: an attachment needs an expense to belong to, so refusing
to create one without a receipt would make the receipt impossible to add. It is
therefore a visible mark on the claim, and a hard refusal when the week is
submitted ([ADR-0023](0023-week-as-the-unit-of-approval.md)) — the point at
which the claim is actually being made, and the last point at which it is cheap
to fix.

## Consequences

**Positive**

* An overtime hour is overtime because somebody said so, and the record says who
  and when.
* A customer with no rules set bills exactly as it did before the feature
  existed, so nothing changes for anyone who does not need it.
* Mileage and per diem stop being mental arithmetic, and the working is on the
  claim.
* The frozen-amount rule is preserved: an entry's value still depends only on
  the entry.

**Negative / accepted costs**

* **The person has to remember to mark it.** This is the whole trade: the notice
  reduces the cost of forgetting, but a week reviewed carelessly can still be
  submitted with overtime unmarked, and then it bills at the ordinary rate. The
  alternative fails in the more expensive direction.
* **Rules live on the customer with no history.** Changing a multiplier changes
  what *future* entries are worth; already-recorded entries keep their frozen
  amounts, which is correct, but there is no record in this table of what the
  terms used to be. A dated rules table is the obvious extension and is not built.
* **One set of terms per customer.** A customer with different overtime terms per
  project cannot express that. Rates have a four-level hierarchy; these do not,
  because a contract sets overtime for the engagement rather than per assignment
  — but a customer with two contracts needs two customer records.
* **The overtime notice reads a week of entries in Go** rather than aggregating
  in SQL, because the week an entry belongs to depends on the owner's zone and
  the instance's week-start day. For a week of one person's entries this is
  nothing; it would not be the right shape for a year.
* Travel that is recorded but never invoiced still appears in the "tracked"
  total, because it *is* time worked. Somebody comparing tracked hours with
  invoiced hours will see the difference, which is the intent.

## Alternatives considered

**Derive overtime from the threshold and reclassify automatically.** Rejected
above: it makes a contractual decision on the user's behalf, and it makes an
entry's value depend on its neighbours.

**Compute overtime at report time** rather than storing it on the entry. It
would let a threshold be applied retroactively and consistently. Rejected: it
breaks the frozen-amount rule, so a report re-run next year could produce a
different figure from the one that was invoiced.

**Overtime as a boolean flag** rather than a kind. Simpler, and it does not
extend: travel is the second case, and there will be a third. An enumeration
costs the same and does.

**A separate rate hierarchy for overtime**, mirroring the four levels the base
rate has. Rejected as disproportionate — a contract sets overtime for the
engagement, and four levels of inheritance for a rule most customers never set
would be more machinery than the problem has.

**Mileage as a category of ordinary expense**, with the user typing the amount.
Simplest of all, and it is what the application did before. Rejected because the
multiplication is exactly the sort of thing people get wrong, and the resulting
error goes to a customer.

## Related

* [ADR-0014](0014-exact-money-and-duration.md) — integer money and durations;
  the quantity is thousandths for the same reason
* [ADR-0023](0023-week-as-the-unit-of-approval.md) — submission, where the
  receipt requirement is enforced
* [ADR-0004](0004-concurrent-timers.md) — overlapping entries, which is why a
  day's total can exceed its elapsed span before overtime is even considered
